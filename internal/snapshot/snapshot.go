// Package snapshot implements the fast SessionStart workspace context hook
// and the live statusline renderer for Antigravity CLI.
package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aliothmoon/mcplex/pkg/output"
	ptcruntime "github.com/aliothmoon/mcplex/pkg/runtime"
)

var noisyDirs = map[string]bool{
	".git":              true,
	"node_modules":      true,
	"vendor":            true,
	"dist":              true,
	"build":             true,
	"target":            true,
	".next":             true,
	".nuxt":             true,
	"__pycache__":       true,
	".venv":             true,
	"venv":              true,
	"coverage":          true,
	"playwright-report": true,
	"tmp":               true,
	"logs":              true,
}

var priorityDirNames = map[string]int{
	"cmd":      1,
	"internal": 2,
	"pkg":      3,
	"src":      4,
	"app":      5,
	"packages": 6,
	"plugin":   7,
	"scripts":  8,
	"test":     9,
	"tests":    10,
}

type sessionStartInput struct {
	Cwd            string   `json:"cwd"`
	WorkspacePaths []string `json:"workspacePaths"`
	WorkspaceRoots []string `json:"workspaceRoots"`
}

// RunSessionStartHook reads Antigravity's SessionStart stdin JSON and writes a compact <=1.5KB workspace snapshot.
// Always fails open (`{}`) on any error so session startup is never blocked.
func RunSessionStartHook(in io.Reader, out io.Writer) {
	raw, err := readBounded(in, 1500*time.Millisecond)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		_, _ = out.Write([]byte("{}\n"))
		return
	}
	var payload sessionStartInput
	if json.Unmarshal(raw, &payload) != nil {
		_, _ = out.Write([]byte("{}\n"))
		return
	}
	ws := strings.TrimSpace(payload.Cwd)
	if ws == "" && len(payload.WorkspacePaths) > 0 {
		ws = strings.TrimSpace(payload.WorkspacePaths[0])
	}
	if ws == "" && len(payload.WorkspaceRoots) > 0 {
		ws = strings.TrimSpace(payload.WorkspaceRoots[0])
	}
	if ws == "" {
		_, _ = out.Write([]byte("{}\n"))
		return
	}

	if os.Getenv("AGY_PTC_DRY_RUN_HOOK") != "1" && !isPluginDirectory(ws) {
		persistActiveWorkspaceHint(ws)
	}

	snap := BuildWorkspaceSnapshot(ws, 1500)
	if snap == "" {
		_, _ = out.Write([]byte("{}\n"))
		return
	}

	resp := map[string]any{
		"injectSteps": []map[string]any{
			{
				"systemMessage": map[string]any{
					"systemMessage": snap,
				},
			},
		},
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		_, _ = out.Write([]byte("{}\n"))
		return
	}
	_, _ = out.Write(append(encoded, '\n'))
}

// BuildWorkspaceSnapshot constructs a budgeted <= maxBytes snapshot of manifests, git state, and 2-level layout.
func BuildWorkspaceSnapshot(workspaceDir string, maxBytes int) string {
	info, err := os.Stat(workspaceDir)
	if err != nil || !info.IsDir() {
		return ""
	}
	if maxBytes <= 0 {
		maxBytes = 1500
	}

	var sections []string
	sections = append(sections, fmt.Sprintf("[agy-ptc workspace snapshot: %s]", filepath.ToSlash(workspaceDir)))

	if proj := detectProjectSummary(workspaceDir); proj != "" {
		sections = append(sections, output.TruncateWithByteNotice("Project: "+proj, 340))
	}
	if gitInfo := detectGitSummary(workspaceDir); gitInfo != "" {
		sections = append(sections, output.TruncateWithByteNotice(gitInfo, 480))
	}
	if layout := buildPrioritizedLayout(workspaceDir); layout != "" {
		sections = append(sections, output.TruncateWithByteNotice("Layout:\n"+layout, 580))
	}

	return output.FitSectionsToByteBudget(sections, maxBytes, "\n")
}

var (
	gradleRootNameRe = regexp.MustCompile(`rootProject\.name\s*=\s*["']([^"']+)["']`)
	gradleIncludeRe  = regexp.MustCompile(`["'](:?[A-Za-z0-9_.:-]+)["']`)
	tomlKeyStringRe  = regexp.MustCompile(`^([A-Za-z0-9_-]+)\s*=\s*["']([^"']+)["']`)
	quotedItemRe     = regexp.MustCompile(`["']([^"']+)["']`)
)

func readFileHead(path string, maxBytes int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, maxBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return buf[:n], nil
}

func detectProjectSummary(ws string) string {
	var parts []string
	if raw, err := readFileHead(filepath.Join(ws, "go.mod"), 16*1024); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "module ") || strings.HasPrefix(t, "go ") {
				parts = append(parts, t)
			}
		}
	}
	if raw, err := readFileHead(filepath.Join(ws, "go.work"), 8*1024); err == nil {
		var mods []string
		inUse := false
		for _, line := range strings.Split(string(raw), "\n") {
			t := strings.TrimSpace(strings.SplitN(line, "//", 2)[0])
			if t == "" {
				continue
			}
			if strings.HasPrefix(t, "use (") {
				inUse = true
				continue
			}
			if inUse {
				if t == ")" {
					inUse = false
					continue
				}
				mods = append(mods, strings.Trim(t, `"`))
			} else if strings.HasPrefix(t, "use ") {
				mods = append(mods, strings.Trim(strings.TrimSpace(strings.TrimPrefix(t, "use ")), `"`))
			}
		}
		if len(mods) > 6 {
			mods = mods[:6]
		}
		if len(mods) > 0 {
			parts = append(parts, fmt.Sprintf("go.work=[%s]", strings.Join(mods, ",")))
		}
	}
	if raw, err := readFileHead(filepath.Join(ws, "package.json"), 32*1024); err == nil {
		var pkg struct {
			Name           string            `json:"name"`
			PackageManager string            `json:"packageManager"`
			Scripts        map[string]string `json:"scripts"`
		}
		if json.Unmarshal(raw, &pkg) == nil {
			var scriptKeys []string
			for k := range pkg.Scripts {
				scriptKeys = append(scriptKeys, k)
			}
			sort.Strings(scriptKeys)
			if len(scriptKeys) > 8 {
				scriptKeys = scriptKeys[:8]
			}
			pm := "npm"
			if idx := strings.IndexByte(pkg.PackageManager, '@'); idx > 0 {
				pm = pkg.PackageManager[:idx]
			}
			if pkg.Name != "" || len(scriptKeys) > 0 {
				parts = append(parts, fmt.Sprintf("%s(%s) scripts=[%s]", pm, pkg.Name, strings.Join(scriptKeys, ",")))
			}
		}
	}
	if gSummary := detectGradleSummary(ws); gSummary != "" {
		parts = append(parts, gSummary)
	}
	if cSummary := detectCargoSummary(ws); cSummary != "" {
		parts = append(parts, cSummary)
	}
	if pySummary := detectPyprojectSummary(ws); pySummary != "" {
		parts = append(parts, pySummary)
	}
	for _, marker := range []string{"CMakeLists.txt", "Makefile", "Taskfile.yml"} {
		if _, err := os.Stat(filepath.Join(ws, marker)); err == nil {
			parts = append(parts, marker)
		}
	}
	return strings.Join(parts, " | ")
}

func detectGradleSummary(ws string) string {
	for _, name := range []string{"settings.gradle.kts", "settings.gradle"} {
		raw, err := readFileHead(filepath.Join(ws, name), 24*1024)
		if err != nil {
			continue
		}
		text := string(raw)
		rootName := ""
		if m := gradleRootNameRe.FindStringSubmatch(text); m != nil {
			rootName = m[1]
		}
		var mods []string
		seen := map[string]bool{}
		for _, line := range strings.Split(text, "\n") {
			t := strings.TrimSpace(strings.SplitN(line, "//", 2)[0])
			if !strings.HasPrefix(t, "include") {
				continue
			}
			for _, im := range gradleIncludeRe.FindAllStringSubmatch(t, -1) {
				mod := im[1]
				if mod != "" && !seen[mod] {
					seen[mod] = true
					mods = append(mods, mod)
					if len(mods) >= 8 {
						break
					}
				}
			}
			if len(mods) >= 8 {
				break
			}
		}
		if rootName != "" && len(mods) > 0 {
			return fmt.Sprintf("gradle(%s) modules=[%s]", rootName, strings.Join(mods, ","))
		}
		if rootName != "" {
			return fmt.Sprintf("gradle(%s)", rootName)
		}
		if len(mods) > 0 {
			return fmt.Sprintf("gradle modules=[%s]", strings.Join(mods, ","))
		}
		return name
	}
	for _, name := range []string{"build.gradle.kts", "build.gradle"} {
		if _, err := os.Stat(filepath.Join(ws, name)); err == nil {
			return name
		}
	}
	return ""
}

func detectCargoSummary(ws string) string {
	raw, err := readFileHead(filepath.Join(ws, "Cargo.toml"), 24*1024)
	if err != nil {
		return ""
	}
	section := ""
	pkgName := ""
	inMembers := false
	var members []string
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			section = strings.TrimSpace(t[1 : len(t)-1])
			inMembers = false
			continue
		}
		if section == "package" && pkgName == "" {
			if m := tomlKeyStringRe.FindStringSubmatch(t); m != nil && m[1] == "name" {
				pkgName = m[2]
			}
		}
		if section == "workspace" {
			if strings.HasPrefix(t, "members") && strings.Contains(t, "[") {
				inMembers = true
			}
			if inMembers {
				for _, q := range quotedItemRe.FindAllStringSubmatch(t, -1) {
					if len(members) < 8 {
						members = append(members, q[1])
					}
				}
				if strings.Contains(t, "]") {
					inMembers = false
				}
			}
		}
	}
	if len(members) > 0 {
		if pkgName != "" {
			return fmt.Sprintf("cargo(%s) workspace=[%s]", pkgName, strings.Join(members, ","))
		}
		return fmt.Sprintf("cargo workspace=[%s]", strings.Join(members, ","))
	}
	if pkgName != "" {
		return fmt.Sprintf("cargo(%s)", pkgName)
	}
	return "Cargo.toml"
}

func detectPyprojectSummary(ws string) string {
	raw, err := readFileHead(filepath.Join(ws, "pyproject.toml"), 24*1024)
	if err != nil {
		return ""
	}
	text := string(raw)
	section := ""
	projName := ""
	toolSet := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			section = strings.TrimSpace(t[1 : len(t)-1])
			for _, tool := range []string{"uv", "poetry", "ruff", "pytest", "mypy"} {
				if section == "tool."+tool || strings.HasPrefix(section, "tool."+tool+".") {
					toolSet[tool] = true
				}
			}
			continue
		}
		if (section == "project" || section == "tool.poetry") && projName == "" {
			if m := tomlKeyStringRe.FindStringSubmatch(t); m != nil && m[1] == "name" {
				projName = m[2]
			}
		}
	}
	var tools []string
	for _, tool := range []string{"uv", "poetry", "ruff", "pytest", "mypy"} {
		if toolSet[tool] {
			tools = append(tools, tool)
		}
	}
	if projName != "" && len(tools) > 0 {
		return fmt.Sprintf("python(%s) tools=[%s]", projName, strings.Join(tools, ","))
	}
	if projName != "" {
		return fmt.Sprintf("python(%s)", projName)
	}
	return "pyproject.toml"
}

func detectGitSummary(ws string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()

	var (
		statusOut []byte
		statusErr error
		logOut    []byte
		wg        sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		statusCmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "--no-pager", "status", "--porcelain=v1", "-b")
		statusCmd.Dir = ws
		statusOut, statusErr = statusCmd.Output()
	}()
	go func() {
		defer wg.Done()
		logCmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "--no-pager", "log", "-n", "3", "--oneline")
		logCmd.Dir = ws
		logOut, _ = logCmd.Output()
	}()
	wg.Wait()

	if statusErr != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(statusOut)), "\n")
	branch := "HEAD"
	dirtyCount := 0
	var sampleDirty []string
	for _, l := range lines {
		if strings.HasPrefix(l, "## ") {
			branch = strings.TrimSpace(strings.TrimPrefix(l, "## "))
		} else if strings.TrimSpace(l) != "" {
			dirtyCount++
			if len(sampleDirty) < 5 {
				sampleDirty = append(sampleDirty, strings.TrimSpace(l))
			}
		}
	}

	commits := strings.ReplaceAll(strings.TrimSpace(string(logOut)), "\n", " ; ")

	dirtySummary := "clean"
	if dirtyCount > 0 {
		dirtySummary = fmt.Sprintf("%d dirty (%s)", dirtyCount, strings.Join(sampleDirty, ", "))
	}
	if commits != "" {
		return fmt.Sprintf("Git: %s | %s | recent: %s", branch, dirtySummary, commits)
	}
	return fmt.Sprintf("Git: %s | %s", branch, dirtySummary)
}

func buildPrioritizedLayout(ws string) string {
	entries, err := os.ReadDir(ws)
	if err != nil {
		return ""
	}
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		pi, okI := priorityDirNames[entries[i].Name()]
		pj, okJ := priorityDirNames[entries[j].Name()]
		if okI != okJ {
			return okI
		}
		if okI && okJ && pi != pj {
			return pi < pj
		}
		return entries[i].Name() < entries[j].Name()
	})

	var items []string
	for _, e := range entries {
		name := e.Name()
		if noisyDirs[name] {
			continue
		}
		if e.IsDir() {
			subEntries, _ := os.ReadDir(filepath.Join(ws, name))
			var kids []string
			for _, se := range subEntries {
				if noisyDirs[se.Name()] {
					continue
				}
				if se.IsDir() {
					kids = append(kids, se.Name()+"/")
				} else {
					kids = append(kids, se.Name())
				}
				if len(kids) >= 6 {
					break
				}
			}
			if len(kids) > 0 {
				items = append(items, fmt.Sprintf("  %s/ [%s]", name, strings.Join(kids, ", ")))
			} else {
				items = append(items, fmt.Sprintf("  %s/", name))
			}
		} else {
			items = append(items, "  "+name)
		}
		if len(items) >= 16 {
			break
		}
	}
	return strings.Join(items, "\n")
}

func sanitizeStatusField(s string, maxRunes int) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	inESC := false
	runes := 0
	for i := 0; i < len(s); i++ {
		if inESC {
			c := s[i]
			if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
				inESC = false
			}
			continue
		}
		if s[i] == 0x1b {
			inESC = true
			continue
		}
		if s[i] == '\r' || s[i] == '\n' || s[i] == '\t' {
			b.WriteByte(' ')
			runes++
			continue
		}
		if s[i] < 0x20 || s[i] == 0x7f {
			continue
		}
		b.WriteByte(s[i])
	}
	clean := strings.Join(strings.Fields(b.String()), " ")
	r := []rune(clean)
	if maxRunes > 0 && len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return clean
}

// RunStatusLine renders a compact 1-line TUI status bar for Antigravity CLI (`stack_with_default: true`).
func RunStatusLine(in io.Reader, out io.Writer, dataDir string) {
	_, _ = readBounded(in, 200*time.Millisecond)
	if dataDir == "" {
		if env := os.Getenv("PLUGIN_DATA"); env != "" {
			dataDir = env
		}
	}
	var st ptcruntime.LiveStatus
	if dataDir != "" {
		if raw, err := os.ReadFile(filepath.Join(dataDir, "live-status.json")); err == nil {
			_ = json.Unmarshal(raw, &st)
		}
	}
	if st.Active && isLiveStatusStale(st.UpdatedAt, time.Now()) {
		st.Active = false
	}
	title := sanitizeStatusField(st.Title, 64)
	if title == "" {
		title = "vm_exec"
	}
	if st.Active {
		op := sanitizeStatusField(st.CurrentOp, 64)
		if op == "" {
			op = title
		}
		fmt.Fprintf(out, "\x1b[36m⚡ PTC running:\x1b[0m %s · %s (runs: %d, host ops: %d)\n", title, op, st.TotalRuns, st.TotalHostOps)
		return
	}
	if st.TotalRuns > 0 {
		badge := "\x1b[32m● PTC\x1b[0m"
		if st.LastStatus == "error" {
			badge = "\x1b[31m● PTC (err)\x1b[0m"
		}
		fmt.Fprintf(out, "%s %d runs · %d host ops compressed · last: %s (%dms)\n", badge, st.TotalRuns, st.TotalHostOps, title, st.LastDurMs)
		return
	}
	fmt.Fprintf(out, "\x1b[90m○ PTC ready (Code Mode active)\x1b[0m\n")
}

func readBounded(r io.Reader, timeout time.Duration) ([]byte, error) {
	type res struct {
		data []byte
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		b, err := io.ReadAll(io.LimitReader(r, 256*1024))
		ch <- res{data: b, err: err}
	}()
	select {
	case out := <-ch:
		return out.data, out.err
	case <-time.After(timeout):
		return nil, context.DeadlineExceeded
	}
}

// persistActiveWorkspaceHint records the session's workspace directory into plugin_data so the resident MCP server
// can resolve relative paths accurately even if an LLM call ever omits `cwd` (since Antigravity spawns plugin MCP servers with cwd=<plugin-root>).
func persistActiveWorkspaceHint(ws string) {
	abs, err := filepath.Abs(ws)
	if err != nil {
		abs = ws
	}
	payload, err := json.Marshal(map[string]string{
		"cwd":       abs,
		"updatedAt": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	var dirs []string
	if pd := strings.TrimSpace(os.Getenv("PLUGIN_DATA")); pd != "" {
		dirs = append(dirs, pd)
	} else if h, hErr := os.UserHomeDir(); hErr == nil && h != "" {
		dirs = append(dirs,
			filepath.Join(h, ".gemini", "antigravity-cli", "plugin_data", "agy-ptc"),
			filepath.Join(h, ".gemini", "antigravity", "plugin_data", "agy-ptc"),
		)
	}
	for _, d := range dirs {
		if os.MkdirAll(d, 0o755) != nil {
			continue
		}
		target := filepath.Join(d, "active-workspace.json")
		tmp, tmpErr := os.CreateTemp(d, ".active-workspace-*.tmp")
		if tmpErr != nil {
			continue
		}
		tmpPath := tmp.Name()
		if _, wErr := tmp.Write(payload); wErr == nil && tmp.Close() == nil {
			_ = os.Chmod(tmpPath, 0o600)
			if os.Rename(tmpPath, target) != nil {
				_ = os.Remove(tmpPath)
			}
		} else {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}
}

func isPluginDirectory(ws string) bool {
	abs, err := filepath.Abs(ws)
	if err != nil {
		abs = ws
	}
	norm := strings.ToLower(filepath.ToSlash(filepath.Clean(abs)))
	if pr := strings.TrimSpace(os.Getenv("PLUGIN_ROOT")); pr != "" {
		if prAbs, err := filepath.Abs(pr); err == nil {
			pr = prAbs
		}
		normPR := strings.ToLower(filepath.ToSlash(filepath.Clean(pr)))
		if norm == normPR || strings.HasPrefix(norm, normPR+"/") {
			return true
		}
	}
	if strings.Contains(norm, "/.gemini/config/plugins/agy-ptc") || strings.Contains(norm, "/.agents/plugins/agy-ptc") {
		return true
	}
	if raw, err := os.ReadFile(filepath.Join(abs, "plugin.json")); err == nil {
		var meta struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &meta) == nil && strings.EqualFold(strings.TrimSpace(meta.Name), "agy-ptc") {
			return true
		}
	}
	return false
}

const staleLiveStatusThreshold = 150 * time.Second

func isLiveStatusStale(updatedAt string, now time.Time) bool {
	s := strings.TrimSpace(updatedAt)
	if s == "" {
		return true
	}
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		ts, err = time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return true
		}
	}
	return now.Sub(ts) > staleLiveStatusThreshold
}
