package capabilities

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aliothmoon/agy-ptc/internal/policy"
	"github.com/aliothmoon/agy-ptc/internal/sandbox"
)

const (
	MaxSandboxReadBytes = 2 * 1024 * 1024 // 2 MB per file inside sandbox memory (never directly enters LLM context)
)

var skippedWalkDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".next":        true,
	".nuxt":        true,
	"dist":         true,
	"build":        true,
	"target":       true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
}

// SearchHit represents a single structured search match returned to guest TypeScript.
type SearchHit struct {
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Column  int      `json:"column,omitempty"`
	Text    string   `json:"text"`
	Before  []string `json:"before,omitempty"`
	After   []string `json:"after,omitempty"`
	Subtext string   `json:"match,omitempty"`
}

// FSSearchBinder exposes `fs.*` and `search.*` to guest programs.
type FSSearchBinder struct {
	Policy *policy.Engine
	hasRg  sync.Once
	rgBin  string
}

func (b *FSSearchBinder) resolveRg() string {
	b.hasRg.Do(func() {
		if p, err := exec.LookPath("rg"); err == nil {
			b.rgBin = p
		}
	})
	return b.rgBin
}

func (b *FSSearchBinder) Bind(env *sandbox.ExecutionEnv) error {
	fsAsync := map[string]sandbox.AsyncHostFunc{
		"read": func(_ context.Context, args []any) (any, error) {
			targetPath, opts, err := parsePathAndOpts(args)
			if err != nil {
				return nil, err
			}
			absPath, err := b.Policy.ResolveAndCheckRead(env.Cwd, targetPath)
			if err != nil {
				return nil, err
			}
			info, err := os.Stat(absPath)
			if err != nil {
				return nil, err
			}
			if info.IsDir() {
				return nil, fmt.Errorf("path '%s' is a directory; use fs.list() or fs.tree()", targetPath)
			}
			if info.Size() > MaxSandboxReadBytes {
				return nil, fmt.Errorf("file '%s' (%d bytes) exceeds 2MB sandbox read cap; use search.rg() or pass startLine/endLine via fs.readLines()", targetPath, info.Size())
			}
			raw, err := os.ReadFile(absPath)
			if err != nil {
				return nil, err
			}
			if bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
				return nil, fmt.Errorf("file '%s' appears to be binary", targetPath)
			}
			text := strings.ReplaceAll(string(raw), "\r\n", "\n")
			startLine := optInt(opts, "startLine", optInt(opts, "offset", 0))
			endLine := optInt(opts, "endLine", 0)
			limit := optInt(opts, "limit", 0)
			if startLine > 0 || endLine > 0 || limit > 0 {
				lines := strings.Split(text, "\n")
				s := maxInt(1, startLine)
				e := len(lines)
				if endLine > 0 && endLine < e {
					e = endLine
				} else if limit > 0 && s+limit-1 < e {
					e = s + limit - 1
				}
				if s > len(lines) {
					return "", nil
				}
				if e < s {
					return "", fmt.Errorf("invalid range: startLine (%d) > endLine (%d)", s, e)
				}
				withNumbers := optBool(opts, "lineNumbers", false)
				selected := lines[s-1 : e]
				if withNumbers {
					numbered := make([]string, len(selected))
					for i, l := range selected {
						numbered[i] = fmt.Sprintf("%d: %s", s+i, l)
					}
					return strings.Join(numbered, "\n"), nil
				}
				return strings.Join(selected, "\n"), nil
			}
			return text, nil
		},
		"write": func(_ context.Context, args []any) (any, error) {
			var targetPath, content string
			if len(args) >= 1 {
				if m, ok := args[0].(map[string]any); ok {
					targetPath = firstString(m, "path", "file", "file_path")
					content = firstString(m, "content", "text", "contents", "body")
				} else if s, ok := args[0].(string); ok {
					targetPath = s
					if len(args) >= 2 {
						if c, ok := args[1].(string); ok {
							content = c
						}
					}
				}
			}
			if targetPath == "" {
				return nil, errors.New("fs.write requires path and content")
			}
			absPath, err := b.Policy.ResolveAndCheckWrite(env.Cwd, targetPath)
			if err != nil {
				return nil, err
			}
			if err := writeFilePreserveMode(absPath, []byte(content)); err != nil {
				return nil, err
			}
			return map[string]any{
				"ok":    true,
				"path":  relDisplayPath(env.Cwd, absPath),
				"bytes": len(content),
			}, nil
		},
		"exists": func(_ context.Context, args []any) (any, error) {
			targetPath, err := requireStringArg(args, 0, "path")
			if err != nil {
				return nil, err
			}
			absPath, err := b.Policy.ResolveAndCheckRead(env.Cwd, targetPath)
			if err != nil {
				return false, nil
			}
			_, statErr := os.Stat(absPath)
			return statErr == nil, nil
		},
		"stat": func(_ context.Context, args []any) (any, error) {
			targetPath, err := requireStringArg(args, 0, "path")
			if err != nil {
				return nil, err
			}
			absPath, err := b.Policy.ResolveAndCheckRead(env.Cwd, targetPath)
			if err != nil {
				return nil, err
			}
			info, err := os.Stat(absPath)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"path":    relDisplayPath(env.Cwd, absPath),
				"size":    info.Size(),
				"isDir":   info.IsDir(),
				"modTime": info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
			}, nil
		},
		"list": func(_ context.Context, args []any) (any, error) {
			targetPath := "."
			var opts map[string]any
			if len(args) > 0 && args[0] != nil {
				targetPath, opts, _ = parsePathAndOpts(args)
				if targetPath == "" {
					targetPath = "."
				}
			}
			absPath, err := b.Policy.ResolveAndCheckRead(env.Cwd, targetPath)
			if err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(absPath)
			if err != nil {
				return nil, err
			}
			limit := optInt(opts, "limit", 200)
			var items []map[string]any
			for _, e := range entries {
				if len(items) >= limit {
					break
				}
				info, _ := e.Info()
				var sz int64
				if info != nil {
					sz = info.Size()
				}
				items = append(items, map[string]any{
					"name":  e.Name(),
					"isDir": e.IsDir(),
					"size":  sz,
				})
			}
			return items, nil
		},
		"tree": func(_ context.Context, args []any) (any, error) {
			targetPath := "."
			maxDepth := 2
			if len(args) > 0 && args[0] != nil {
				p, opts, _ := parsePathAndOpts(args)
				if p != "" {
					targetPath = p
				}
				maxDepth = optInt(opts, "depth", optInt(opts, "maxDepth", 2))
			}
			if len(args) > 1 {
				if d, ok := toInt(args[1]); ok && d > 0 {
					maxDepth = d
				}
			}
			if maxDepth < 1 {
				maxDepth = 1
			}
			if maxDepth > 5 {
				maxDepth = 5
			}
			absPath, err := b.Policy.ResolveAndCheckRead(env.Cwd, targetPath)
			if err != nil {
				return nil, err
			}
			return buildTreeList(absPath, maxDepth, 300), nil
		},
	}

	if err := env.SetNamespace("fs", fsAsync, nil); err != nil {
		return err
	}

	searchAsync := map[string]sandbox.AsyncHostFunc{
		"rg": func(ctx context.Context, args []any) (any, error) {
			q, err := parseSearchQuery(args)
			if err != nil {
				return nil, err
			}
			searchRoot, err := b.Policy.ResolveAndCheckRead(env.Cwd, q.path)
			if err != nil {
				return nil, err
			}
			return b.runTextOrRegexSearch(ctx, env.Cwd, searchRoot, q)
		},
		"glob": func(ctx context.Context, args []any) (any, error) {
			pat, opts, err := parsePatternAndOpts(args)
			if err != nil {
				return nil, err
			}
			subPath := firstString(opts, "path", "dir", "cwd")
			if subPath == "" {
				subPath = "."
			}
			searchRoot, err := b.Policy.ResolveAndCheckRead(env.Cwd, subPath)
			if err != nil {
				return nil, err
			}
			maxResults := optInt(opts, "maxResults", optInt(opts, "limit", 200))
			files, note, err := b.runGlobSearch(ctx, env.Cwd, searchRoot, pat, maxResults)
			if err != nil {
				return nil, err
			}
			if optBool(opts, "withMeta", false) {
				return map[string]any{"files": files, "note": note}, nil
			}
			return files, nil
		},
	}
	return env.SetNamespace("search", searchAsync, nil)
}

type searchQuery struct {
	pattern       string
	path          string
	glob          string
	literal       bool
	caseSensitive bool
	contextLines  int
	maxResults    int
}

func parseSearchQuery(args []any) (searchQuery, error) {
	q := searchQuery{
		path:          ".",
		caseSensitive: true,
		maxResults:    100,
	}
	if len(args) == 0 || args[0] == nil {
		return q, errors.New("search.rg requires a pattern string or options object")
	}
	var opts map[string]any
	if s, ok := args[0].(string); ok {
		q.pattern = s
		if len(args) > 1 {
			if m, ok := args[1].(map[string]any); ok {
				opts = m
			} else if p, ok := args[1].(string); ok && p != "" {
				q.path = p
			}
		}
	} else if m, ok := args[0].(map[string]any); ok {
		opts = m
		q.pattern = firstString(m, "pattern", "query", "regex", "text", "q")
	}
	if q.pattern == "" {
		return q, errors.New("search.rg: 'pattern' cannot be empty")
	}
	if opts != nil {
		if p := firstString(opts, "path", "dir", "directory"); p != "" {
			q.path = p
		}
		q.glob = firstString(opts, "glob", "globPattern", "include")
		q.literal = optBool(opts, "literal", optBool(opts, "fixedStrings", false))
		if optBool(opts, "ignoreCase", optBool(opts, "ic", false)) {
			q.caseSensitive = false
		} else if v, exists := opts["caseSensitive"]; exists {
			if b, ok := v.(bool); ok {
				q.caseSensitive = b
			}
		}
		q.contextLines = optInt(opts, "contextLines", optInt(opts, "context", 0))
		if q.contextLines > 5 {
			q.contextLines = 5
		}
		q.maxResults = optInt(opts, "maxResults", optInt(opts, "limit", 100))
		if q.maxResults <= 0 {
			q.maxResults = 100
		}
		if q.maxResults > 500 {
			q.maxResults = 500
		}
	}
	return q, nil
}

func (b *FSSearchBinder) runTextOrRegexSearch(ctx context.Context, workspaceCwd, searchRoot string, q searchQuery) ([]SearchHit, error) {
	if rgBin := b.resolveRg(); rgBin != "" {
		hits, err := runRgJSON(ctx, rgBin, workspaceCwd, searchRoot, q)
		if err == nil {
			return hits, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return runGoFallbackSearch(ctx, workspaceCwd, searchRoot, q)
}

func runRgJSON(ctx context.Context, rgBin, workspaceCwd, searchRoot string, q searchQuery) ([]SearchHit, error) {
	rgArgs := []string{"--json", "--line-number", "--column", "--no-heading", "--color=never"}
	if q.literal {
		rgArgs = append(rgArgs, "--fixed-strings")
	}
	if !q.caseSensitive {
		rgArgs = append(rgArgs, "--ignore-case")
	} else {
		rgArgs = append(rgArgs, "--case-sensitive")
	}
	if q.contextLines > 0 {
		rgArgs = append(rgArgs, fmt.Sprintf("-C%d", q.contextLines))
	}
	if q.glob != "" {
		rgArgs = append(rgArgs, "-g", q.glob)
	}
	rgArgs = append(rgArgs, "--", q.pattern, searchRoot)

	rgCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(rgCtx, rgBin, rgArgs...)
	setSysProcAttrHide(cmd)
	stdout := newLimitedSyncBuffer(2 * 1024 * 1024)
	stderr := newLimitedSyncBuffer(128 * 1024)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	// rg exit code 1 means 0 matches; exit code 2 means regex/syntax error.
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && rgCtx.Err() == nil {
			if exitErr.ExitCode() == 1 {
				return []SearchHit{}, nil
			}
			if exitErr.ExitCode() == 2 {
				return nil, fmt.Errorf("invalid search pattern: %s", strings.TrimSpace(stderr.String()))
			}
		}
		if rgCtx.Err() != nil {
			return nil, fmt.Errorf("search.rg timed out: %w", rgCtx.Err())
		}
		return nil, err
	}

	type rgEvent struct {
		Type string `json:"type"`
		Data struct {
			Path struct {
				Text string `json:"text"`
			} `json:"path"`
			Lines struct {
				Text string `json:"text"`
			} `json:"lines"`
			LineNumber int `json:"line_number"`
			Submatches []struct {
				Start int `json:"start"`
				Match struct {
					Text string `json:"text"`
				} `json:"match"`
			} `json:"submatches"`
		} `json:"data"`
	}

	hits := []SearchHit{}
	scanner := bufio.NewScanner(strings.NewReader(stdout.String()))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var ev rgEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Type != "match" {
			continue
		}
		col := 1
		sub := ""
		if len(ev.Data.Submatches) > 0 {
			col = ev.Data.Submatches[0].Start + 1
			sub = ev.Data.Submatches[0].Match.Text
		}
		lineText := strings.TrimRight(ev.Data.Lines.Text, "\r\n")
		if len(lineText) > 280 {
			lineText = lineText[:280] + "..."
		}
		hits = append(hits, SearchHit{
			File:    relDisplayPath(workspaceCwd, ev.Data.Path.Text),
			Line:    ev.Data.LineNumber,
			Column:  col,
			Text:    lineText,
			Subtext: sub,
		})
		if len(hits) >= q.maxResults {
			break
		}
	}
	return hits, nil
}

func runGoFallbackSearch(ctx context.Context, workspaceCwd, searchRoot string, q searchQuery) ([]SearchHit, error) {
	var re *regexp.Regexp
	var err error
	pat := q.pattern
	if q.literal {
		pat = regexp.QuoteMeta(pat)
	}
	if !q.caseSensitive {
		pat = "(?i)" + pat
	}
	re, err = regexp.Compile(pat)
	if err != nil {
		return nil, fmt.Errorf("invalid regex '%s': %w", q.pattern, err)
	}

	var files []string
	info, err := os.Stat(searchRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		files = []string{searchRoot}
	} else {
		_ = filepath.WalkDir(searchRoot, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.IsDir() {
				if p != searchRoot && skippedWalkDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if q.glob != "" && !matchSimpleGlob(q.glob, filepath.ToSlash(p), d.Name()) {
				return nil
			}
			files = append(files, p)
			return nil
		})
	}

	hits := []SearchHit{}
	for _, f := range files {
		if ctx.Err() != nil || len(hits) >= q.maxResults {
			break
		}
		fi, err := os.Stat(f)
		if err != nil || fi.Size() > MaxSandboxReadBytes {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil || bytes.IndexByte(raw, 0) >= 0 {
			continue
		}
		lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
		rel := relDisplayPath(workspaceCwd, f)
		for idx, line := range lines {
			loc := re.FindStringIndex(line)
			if loc == nil {
				continue
			}
			var before, after []string
			if q.contextLines > 0 {
				for b := maxInt(0, idx-q.contextLines); b < idx; b++ {
					before = append(before, lines[b])
				}
				for a := idx + 1; a <= minInt(len(lines)-1, idx+q.contextLines); a++ {
					after = append(after, lines[a])
				}
			}
			txt := line
			if len(txt) > 280 {
				txt = txt[:280] + "..."
			}
			hits = append(hits, SearchHit{
				File:    rel,
				Line:    idx + 1,
				Column:  loc[0] + 1,
				Text:    txt,
				Before:  before,
				After:   after,
				Subtext: line[loc[0]:loc[1]],
			})
			if len(hits) >= q.maxResults {
				break
			}
		}
	}
	return hits, nil
}

func (b *FSSearchBinder) runGlobSearch(ctx context.Context, workspaceCwd, searchRoot, pattern string, maxResults int) ([]string, string, error) {
	if maxResults <= 0 {
		maxResults = 200
	}
	allFiles := collectWorkspaceFiles(ctx, searchRoot)
	matchFiles := func(pat string) []string {
		var matched []string
		for _, abs := range allFiles {
			relToSearch, err := filepath.Rel(searchRoot, abs)
			if err != nil {
				relToSearch = abs
			}
			relSlash := filepath.ToSlash(relToSearch)
			if matchGlobPattern(pat, relSlash) {
				matched = append(matched, relDisplayPath(workspaceCwd, abs))
				if len(matched) >= maxResults {
					break
				}
			}
		}
		sort.Strings(matched)
		return matched
	}

	exact := matchFiles(pattern)
	if len(exact) > 0 {
		return exact, "", nil
	}

	// Smart fallback 1 (inspired by agy-fast v0.2.2): expand regex alternation `a|b` mistakenly passed to glob
	if strings.Contains(pattern, "|") && !strings.Contains(pattern, "{") {
		expanded := "*{" + strings.ReplaceAll(pattern, "|", ",") + "}*"
		recovered := matchFiles(expanded)
		if len(recovered) > 0 {
			return recovered, fmt.Sprintf("auto-expanded regex '|' to glob '%s'", expanded), nil
		}
	}

	// Smart fallback 2: bare keyword without wildcards, slashes, or extension dots -> substring match
	if !strings.ContainsAny(pattern, "*?[]{}/\\.") {
		sub := strings.ToLower(pattern)
		var recovered []string
		for _, abs := range allFiles {
			rel := relDisplayPath(workspaceCwd, abs)
			if strings.Contains(strings.ToLower(rel), sub) {
				recovered = append(recovered, rel)
				if len(recovered) >= maxResults {
					break
				}
			}
		}
		sort.Strings(recovered)
		if len(recovered) > 0 {
			return recovered, fmt.Sprintf("bare keyword matched as '*%s*'", pattern), nil
		}
	}

	return []string{}, "", nil
}

func collectWorkspaceFiles(ctx context.Context, root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && skippedWalkDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, p)
		return nil
	})
	return out
}

func matchGlobPattern(pattern, relSlash string) bool {
	// Expand single-level `{a,b,c}` braces
	for _, expanded := range expandBraces(filepath.ToSlash(pattern)) {
		if globSingle(expanded, relSlash) {
			return true
		}
	}
	return false
}

func expandBraces(pat string) []string {
	open := strings.IndexByte(pat, '{')
	closeIdx := strings.IndexByte(pat, '}')
	if open >= 0 && closeIdx > open {
		prefix := pat[:open]
		suffix := pat[closeIdx+1:]
		alts := strings.Split(pat[open+1:closeIdx], ",")
		var out []string
		for _, a := range alts {
			out = append(out, expandBraces(prefix+strings.TrimSpace(a)+suffix)...)
		}
		return out
	}
	return []string{pat}
}

func globSingle(pat, relSlash string) bool {
	if !strings.Contains(pat, "/") {
		ok, _ := path.Match(pat, path.Base(relSlash))
		if ok {
			return true
		}
	}
	regexStr := "^"
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		switch c {
		case '*':
			if i+1 < len(pat) && pat[i+1] == '*' {
				if i+2 < len(pat) && pat[i+2] == '/' {
					regexStr += "(?:.*/)?"
					i += 2
				} else {
					regexStr += ".*"
					i++
				}
			} else {
				regexStr += "[^/]*"
			}
		case '?':
			regexStr += "[^/]"
		case '.', '+', '(', ')', '|', '^', '$', '[', ']', '\\':
			regexStr += "\\" + string(c)
		default:
			regexStr += string(c)
		}
	}
	regexStr += "$"
	re, err := regexp.Compile(regexStr)
	if err != nil {
		return false
	}
	return re.MatchString(relSlash)
}

func matchSimpleGlob(globPat, fullSlash, baseName string) bool {
	return matchGlobPattern(globPat, fullSlash) || matchGlobPattern(globPat, baseName)
}

func buildTreeList(root string, maxDepth, maxItems int) []string {
	var out []string
	var walk func(dir, relPrefix string, depth int)
	walk = func(dir, relPrefix string, depth int) {
		if depth > maxDepth || len(out) >= maxItems {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if len(out) >= maxItems {
				return
			}
			name := e.Name()
			if e.IsDir() && skippedWalkDirs[name] {
				continue
			}
			rel := name
			if relPrefix != "" {
				rel = relPrefix + "/" + name
			}
			if e.IsDir() {
				out = append(out, rel+"/")
				if depth < maxDepth {
					walk(filepath.Join(dir, name), rel, depth+1)
				}
			} else {
				out = append(out, rel)
			}
		}
	}
	walk(root, "", 1)
	return out
}
