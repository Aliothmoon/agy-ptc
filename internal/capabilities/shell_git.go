package capabilities

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/aliothmoon/agy-ptc/internal/output"
	"github.com/aliothmoon/agy-ptc/internal/policy"
	"github.com/aliothmoon/agy-ptc/internal/sandbox"
)

var allowedGitReadSubcommands = map[string]bool{
	"status":    true,
	"diff":      true,
	"log":       true,
	"show":      true,
	"branch":    true,
	"rev-parse": true,
	"ls-files":  true,
	"blame":     true,
	"shortlog":  true,
	"describe":  true,
	"tag":       true,
	"remote":    true,
}

// ShellGitBinder exposes `sh.*` (`exec`, `checks`) and `git.*` (`status`, `diff`, `log`, `show`, `branch`, `blame`, `query`).
type ShellGitBinder struct {
	Policy *policy.Engine
}

func (b *ShellGitBinder) Bind(env *sandbox.ExecutionEnv) error {
	shAsync := map[string]sandbox.AsyncHostFunc{
		"exec": func(ctx context.Context, args []any) (any, error) {
			cmdStr, opts, err := parseCmdAndOpts(args)
			if err != nil {
				return nil, err
			}
			workDir := env.Cwd
			if subCwd := firstString(opts, "cwd"); subCwd != "" {
				resolved, rErr := b.Policy.ResolveAndCheckRead(env.Cwd, subCwd)
				if rErr != nil {
					return nil, rErr
				}
				workDir = resolved
			}
			cmdTimeout := resolveCommandTimeout(opts, DefaultShellTimeout)
			// Default settle=true (pi-fabric native style): non-zero exit codes return `{ ok: false, exitCode, output }`
			// instead of blowing up Promise.all! Callers wanting exceptions can pass `{ throwOnError: true }` or `{ settle: false }`.
			settle := optBool(opts, "settle", true)
			if optBool(opts, "throwOnError", false) {
				settle = false
			}
			compact := optBool(opts, "compact", false)
			res, err := b.RunSingleCommandDuration(ctx, workDir, cmdStr, cmdTimeout, settle, compact)
			if err != nil {
				return nil, err
			}
			return res, nil
		},
		"checks": func(ctx context.Context, args []any) (any, error) {
			return b.RunChecksBatch(ctx, env.Cwd, args)
		},
	}

	if err := env.SetNamespace("sh", shAsync, nil); err != nil {
		return err
	}

	gitAsync := map[string]sandbox.AsyncHostFunc{
		"status": func(ctx context.Context, args []any) (any, error) {
			repoDir, _, err := b.resolveGitDirAndTail(env.Cwd, args)
			if err != nil {
				return nil, err
			}
			return runGitStructuredStatus(ctx, repoDir)
		},
		"diff": func(ctx context.Context, args []any) (any, error) {
			repoDir, extra, err := b.resolveGitDirAndTail(env.Cwd, args)
			if err != nil {
				return nil, err
			}
			return runReadOnlyGitSoft(ctx, repoDir, append([]string{"diff"}, extra...))
		},
		"log": func(ctx context.Context, args []any) (any, error) {
			repoDir, extra, err := b.resolveGitDirAndTail(env.Cwd, args)
			if err != nil {
				return nil, err
			}
			if len(extra) == 0 {
				extra = []string{"-n", "10", "--oneline"}
			}
			return runReadOnlyGitSoft(ctx, repoDir, append([]string{"log"}, extra...))
		},
		"show": func(ctx context.Context, args []any) (any, error) {
			repoDir, extra, err := b.resolveGitDirAndTail(env.Cwd, args)
			if err != nil {
				return nil, err
			}
			if len(extra) == 0 {
				extra = []string{"--stat", "HEAD"}
			}
			return runReadOnlyGitSoft(ctx, repoDir, append([]string{"show"}, extra...))
		},
		"branch": func(ctx context.Context, args []any) (any, error) {
			repoDir, _, err := b.resolveGitDirAndTail(env.Cwd, args)
			if err != nil {
				return nil, err
			}
			return runReadOnlyGitSoft(ctx, repoDir, []string{"branch", "--list", "-v"})
		},
		"blame": func(ctx context.Context, args []any) (any, error) {
			targetPath, opts, err := parsePathAndOpts(args)
			if err != nil {
				return nil, err
			}
			repoDir := env.Cwd
			if subCwd := firstString(opts, "cwd", "repo"); subCwd != "" {
				resolvedRepo, rErr := b.Policy.ResolveAndCheckRead(env.Cwd, subCwd)
				if rErr != nil {
					return nil, rErr
				}
				repoDir = resolvedRepo
			}
			absFile, err := b.Policy.ResolveAndCheckRead(repoDir, targetPath)
			if err != nil {
				return nil, err
			}
			gitArgs := []string{"blame"}
			sLine := optInt(opts, "startLine", 0)
			eLine := optInt(opts, "endLine", 0)
			if sLine > 0 && eLine >= sLine {
				gitArgs = append(gitArgs, fmt.Sprintf("-L%d,%d", sLine, eLine))
			}
			gitArgs = append(gitArgs, "--", absFile)
			return runReadOnlyGitSoft(ctx, filepath.Dir(absFile), gitArgs)
		},
		"query": func(ctx context.Context, args []any) (any, error) {
			rawCmd, err := requireStringArg(args, 0, "subcommand")
			if err != nil {
				return nil, err
			}
			repoDir := env.Cwd
			if len(args) > 1 {
				if m, ok := args[1].(map[string]any); ok {
					if subCwd := firstString(m, "cwd", "repo", "path"); subCwd != "" {
						resolved, rErr := b.Policy.ResolveAndCheckRead(env.Cwd, subCwd)
						if rErr != nil {
							return nil, rErr
						}
						repoDir = resolved
					}
				}
			}
			tokens := splitCommandArgs(rawCmd)
			if len(tokens) > 0 && tokens[0] == "git" {
				tokens = tokens[1:]
			}
			return runReadOnlyGitSoft(ctx, repoDir, tokens)
		},
	}
	return env.SetNamespace("git", gitAsync, nil)
}

const (
	DefaultShellTimeout = 15 * time.Second
	MaxShellTimeout     = 120 * time.Second
)

func resolveCommandTimeout(opts map[string]any, fallback time.Duration) time.Duration {
	if opts == nil {
		return fallback
	}
	var dur time.Duration
	if ms := optInt(opts, "timeoutMs", 0); ms > 0 {
		dur = time.Duration(ms) * time.Millisecond
	} else if sec := optInt(opts, "timeoutSec", 0); sec > 0 {
		dur = time.Duration(sec) * time.Second
	} else if raw := optInt(opts, "timeout", 0); raw > 0 {
		// Node.js child_process convention passes milliseconds (e.g. 5000); values < 500 are seconds.
		if raw >= 500 {
			dur = time.Duration(raw) * time.Millisecond
		} else {
			dur = time.Duration(raw) * time.Second
		}
	} else {
		dur = fallback
	}
	if dur < 100*time.Millisecond {
		dur = 100 * time.Millisecond
	}
	if dur > MaxShellTimeout {
		dur = MaxShellTimeout
	}
	return dur
}

// RunSingleCommand executes one shell command with a timeout in seconds (compatibility wrapper).
func (b *ShellGitBinder) RunSingleCommand(parentCtx context.Context, cwd, cmdLine string, timeoutSec int, settle, compact bool) (map[string]any, error) {
	dur := DefaultShellTimeout
	if timeoutSec > 0 {
		dur = time.Duration(timeoutSec) * time.Second
	}
	return b.RunSingleCommandDuration(parentCtx, cwd, cmdLine, dur, settle, compact)
}

// RunSingleCommandDuration executes one shell command after validating policy rules, killing the entire child process tree on timeout.
func (b *ShellGitBinder) RunSingleCommandDuration(parentCtx context.Context, cwd, cmdLine string, timeoutDur time.Duration, settle, compact bool) (map[string]any, error) {
	if err := b.Policy.CheckCommand(cmdLine); err != nil {
		return nil, err
	}
	if timeoutDur <= 0 {
		timeoutDur = DefaultShellTimeout
	}
	if timeoutDur > MaxShellTimeout {
		timeoutDur = MaxShellTimeout
	}
	ctx, cancel := context.WithTimeout(parentCtx, timeoutDur)
	defer cancel()

	start := time.Now()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		if pwshPath, err := exec.LookPath("pwsh"); err == nil {
			cmd = exec.CommandContext(ctx, pwshPath, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", cmdLine)
		} else {
			cmd = exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", cmdLine)
		}
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", cmdLine)
	}
	cmd.Dir = cwd
	setSysProcAttrHide(cmd)

	stdout := newLimitedSyncBuffer(512 * 1024)
	stderr := newLimitedSyncBuffer(256 * 1024)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	durMs := time.Since(start).Milliseconds()
	parentCanceled := parentCtx.Err() != nil
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(runErr, exec.ErrWaitDelay) || parentCanceled
	exitCode := 0
	spawnErr := ""

	if runErr != nil {
		var exitErr *exec.ExitError
		if timedOut {
			exitCode = -1
		} else if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
			spawnErr = runErr.Error()
		}
	}

	// Inside sandbox memory (compact=false), preserve up to 256 KB so guest TypeScript can JSON.parse(r.output) or filter full stdout without truncation!
	maxCmdBytes := 256 * 1024
	if compact || timedOut || parentCanceled {
		maxCmdBytes = 8000
	}
	formatted := output.FormatCommandOutput(output.CommandFormatParams{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		TimedOut: timedOut,
		SpawnErr: spawnErr,
		MaxBytes: maxCmdBytes,
		Compact:  compact,
	})
	if timedOut {
		formatted = fmt.Sprintf("TIMED OUT after %dms (process tree terminated; for >15s long tasks use built-in run_command + manage_task)\n%s", durMs, strings.TrimPrefix(strings.TrimPrefix(formatted, "TIMED OUT\n"), "TIMED OUT (no output)"))
		formatted = strings.TrimSpace(formatted)
	}

	// If the outer ptc_exec deadline fired, return an error so the sandbox trace captures the partial stdout/stderr for [ptc timeout error]!
	if parentCanceled {
		return nil, fmt.Errorf("%s", formatted)
	}

	ok := exitCode == 0 && !timedOut && spawnErr == ""
	envelope := map[string]any{
		"ok":         ok,
		"exitCode":   exitCode,
		"timedOut":   timedOut,
		"durationMs": durMs,
		"output":     formatted,
	}

	if !ok && !settle {
		return nil, fmt.Errorf("command '%s' failed (exit %d): %s", cmdLine, exitCode, output.TruncateWithByteNotice(formatted, 4000))
	}
	return envelope, nil
}

type checkSpec struct {
	name    string
	command string
	cwd     string
	timeout time.Duration
}

// RunChecksBatch runs multiple build/lint/test commands concurrently and returns a unified pass/fail envelope.
func (b *ShellGitBinder) RunChecksBatch(ctx context.Context, baseCwd string, args []any) (map[string]any, error) {
	if len(args) == 0 || args[0] == nil {
		return nil, errors.New("checks requires an array of commands or { commands: [...] }")
	}
	var specs []checkSpec
	concurrency := 4
	defaultTimeout := DefaultShellTimeout
	compact := true

	var rawList []any
	var opts map[string]any
	if arr, isArr := args[0].([]any); isArr {
		rawList = arr
		if len(args) > 1 {
			opts, _ = args[1].(map[string]any)
		}
	} else if m, isMap := args[0].(map[string]any); isMap {
		opts = m
		rawList, _ = m["commands"].([]any)
	}
	if opts != nil {
		concurrency = optInt(opts, "concurrency", 4)
		defaultTimeout = resolveCommandTimeout(opts, DefaultShellTimeout)
		if mode := firstString(opts, "outputMode"); mode == "full" {
			compact = false
		} else {
			compact = optBool(opts, "compact", true)
		}
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 8 {
		concurrency = 8
	}
	if len(rawList) == 0 {
		return nil, errors.New("commands list cannot be empty")
	}
	if len(rawList) > 12 {
		rawList = rawList[:12]
	}

	for i, item := range rawList {
		switch v := item.(type) {
		case string:
			specs = append(specs, checkSpec{
				name:    fmt.Sprintf("cmd_%d", i+1),
				command: v,
				cwd:     baseCwd,
				timeout: defaultTimeout,
			})
		case map[string]any:
			cmdStr := firstString(v, "command", "cmd")
			if cmdStr == "" {
				return nil, fmt.Errorf("check #%d missing 'command'", i+1)
			}
			subDir := baseCwd
			if c := firstString(v, "cwd"); c != "" {
				resolved, rErr := b.Policy.ResolveAndCheckRead(baseCwd, c)
				if rErr != nil {
					return nil, rErr
				}
				subDir = resolved
			}
			nm := firstString(v, "name")
			if nm == "" {
				nm = cmdStr
				if len(nm) > 36 {
					nm = nm[:36] + "..."
				}
			}
			specs = append(specs, checkSpec{
				name:    nm,
				command: cmdStr,
				cwd:     subDir,
				timeout: resolveCommandTimeout(v, defaultTimeout),
			})
		default:
			return nil, fmt.Errorf("check #%d must be a string or object", i+1)
		}
	}

	// Pre-validate every command against policy BEFORE launching concurrent processes.
	for _, sp := range specs {
		if err := b.Policy.CheckCommand(sp.command); err != nil {
			return nil, err
		}
	}

	results := make([]map[string]any, len(specs))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for idx, sp := range specs {
		wg.Add(1)
		go func(i int, s checkSpec) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r, err := b.RunSingleCommandDuration(ctx, s.cwd, s.command, s.timeout, true, compact)
			if err != nil {
				results[i] = map[string]any{
					"name":     s.name,
					"command":  s.command,
					"ok":       false,
					"exitCode": -1,
					"output":   err.Error(),
				}
				return
			}
			r["name"] = s.name
			r["command"] = s.command
			results[i] = r
		}(idx, sp)
	}
	wg.Wait()

	allOK := true
	var failures []map[string]any
	var summaryParts []string
	for _, r := range results {
		ok, _ := r["ok"].(bool)
		nm, _ := r["name"].(string)
		if !ok {
			allOK = false
			failures = append(failures, r)
			summaryParts = append(summaryParts, fmt.Sprintf("FAIL %s", nm))
		} else {
			summaryParts = append(summaryParts, fmt.Sprintf("PASS %s", nm))
		}
	}

	out := map[string]any{
		"ok":      allOK,
		"summary": strings.Join(summaryParts, " | "),
		"results": results,
	}
	if len(failures) > 0 {
		out["failures"] = failures
	}
	return out, nil
}

// runReadOnlyGitSoft softens benign repository state conditions ("not a git repository", "does not have any commits yet")
// into descriptive strings so exploratory Promise.all([search.rg(...), git.log()]) calls never crash on non-git or empty repos.
// Invalid/disallowed git subcommands or flags still return hard errors.
func runReadOnlyGitSoft(ctx context.Context, cwd string, gitArgs []string) (string, error) {
	out, err := runReadOnlyGit(ctx, cwd, gitArgs)
	if err == nil {
		return out, nil
	}
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "not a git repository") {
		return "(not a git repository)", nil
	}
	if strings.Contains(low, "does not have any commits yet") || strings.Contains(low, "ambiguous argument 'head'") {
		return "(no commits yet)", nil
	}
	return "", err
}

func runReadOnlyGit(ctx context.Context, cwd string, gitArgs []string) (string, error) {
	if len(gitArgs) == 0 {
		return "", errors.New("git subcommand required")
	}
	// Disallow global git flags like -c core.pager=... or --exec-path before the subcommand
	sub := gitArgs[0]
	if strings.HasPrefix(sub, "-") || !allowedGitReadSubcommands[sub] {
		return "", fmt.Errorf("git subcommand '%s' is not in the read-only allowlist (use sh.exec for write git operations)", sub)
	}
	if sub == "tag" {
		hasList := false
		for _, a := range gitArgs[1:] {
			if a == "-l" || a == "--list" {
				hasList = true
			}
		}
		if !hasList && len(gitArgs) > 1 {
			return "", errors.New("only read-only 'git tag --list' is allowed in git.*")
		}
	}
	if sub == "remote" && len(gitArgs) > 1 && gitArgs[1] != "-v" && gitArgs[1] != "show" {
		return "", errors.New("only read-only 'git remote -v' is allowed in git.*")
	}
	for _, a := range gitArgs[1:] {
		if strings.HasPrefix(a, "--upload-pack") || strings.HasPrefix(a, "--exec") || strings.HasPrefix(a, "--output=") {
			return "", fmt.Errorf("disallowed git flag '%s' in read-only git query", a)
		}
	}

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	fullArgs := append([]string{"--no-pager"}, gitArgs...)
	cmd := exec.CommandContext(cctx, "git", fullArgs...)
	cmd.Dir = cwd
	setSysProcAttrHide(cmd)
	stdout := newLimitedSyncBuffer(512 * 1024)
	stderr := newLimitedSyncBuffer(64 * 1024)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if cctx.Err() != nil {
			return "", fmt.Errorf("git %s timed out: %w", sub, cctx.Err())
		}
		return "", fmt.Errorf("git %s failed: %s", sub, strings.TrimSpace(stderr.String()))
	}
	return output.TruncateHeadTailBytes(strings.TrimRight(stdout.String(), "\r\n"), 256*1024, ""), nil
}

func runGitStructuredStatus(ctx context.Context, cwd string) (map[string]any, error) {
	raw, err := runReadOnlyGit(ctx, cwd, []string{"status", "--porcelain=v1", "-b"})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not a git repository") {
			return map[string]any{
				"isRepo":    false,
				"branch":    "",
				"clean":     true,
				"staged":    []string{},
				"modified":  []string{},
				"untracked": []string{},
				"error":     "not a git repository",
			}, nil
		}
		return nil, err
	}
	lines := strings.Split(raw, "\n")
	branch := ""
	var staged, modified, untracked []string
	for _, line := range lines {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			branch = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}
		if len(line) < 3 {
			continue
		}
		x := line[0]
		y := line[1]
		file := strings.TrimSpace(line[3:])
		if x == '?' && y == '?' {
			untracked = append(untracked, file)
			continue
		}
		if x != ' ' && x != '?' {
			staged = append(staged, file)
		}
		if y != ' ' && y != '?' {
			modified = append(modified, file)
		}
	}
	return map[string]any{
		"isRepo":    true,
		"branch":    branch,
		"clean":     len(staged) == 0 && len(modified) == 0 && len(untracked) == 0,
		"staged":    staged,
		"modified":  modified,
		"untracked": untracked,
		"raw":       raw,
	}, nil
}

func parseCmdAndOpts(args []any) (string, map[string]any, error) {
	if len(args) == 0 || args[0] == nil {
		return "", nil, errors.New("sh.exec requires a command string or options object")
	}
	if s, ok := args[0].(string); ok {
		var opts map[string]any
		if len(args) > 1 {
			opts, _ = args[1].(map[string]any)
		}
		return s, opts, nil
	}
	if m, ok := args[0].(map[string]any); ok {
		c := firstString(m, "command", "cmd", "commandLine", "script")
		if c == "" {
			return "", nil, errors.New("sh.exec: 'command' cannot be empty")
		}
		return c, m, nil
	}
	return "", nil, errors.New("invalid sh.exec arguments")
}

// resolveGitDirAndTail supports targeting external repositories via `{ cwd: "D:/OtherRepo", ... }`
// (or passing a directory path to `git.status("D:/OtherRepo")`).
func (b *ShellGitBinder) resolveGitDirAndTail(baseCwd string, args []any) (string, []string, error) {
	if len(args) == 0 || args[0] == nil {
		return baseCwd, nil, nil
	}
	if m, ok := args[0].(map[string]any); ok {
		repoDir := baseCwd
		if sub := firstString(m, "cwd", "repo", "repoPath"); sub != "" {
			resolved, err := b.Policy.ResolveAndCheckRead(baseCwd, sub)
			if err != nil {
				return "", nil, err
			}
			repoDir = resolved
		}
		return repoDir, optionalGitTailArgs(args), nil
	}
	// If first arg is a string with no git flags (e.g. `git.status("D:/OtherRepo")`), check if it's an existing directory
	if s, ok := args[0].(string); ok && !strings.HasPrefix(strings.TrimSpace(s), "-") && !strings.ContainsAny(s, " \t") {
		if resolved, err := b.Policy.ResolveAndCheckRead(baseCwd, s); err == nil {
			if fi, stErr := os.Stat(resolved); stErr == nil && fi.IsDir() {
				var rest []string
				if len(args) > 1 {
					rest = optionalGitTailArgs(args[1:])
				}
				return resolved, rest, nil
			}
		}
	}
	return baseCwd, optionalGitTailArgs(args), nil
}

func optionalGitTailArgs(args []any) []string {
	if len(args) == 0 || args[0] == nil {
		return nil
	}
	if s, ok := args[0].(string); ok && strings.TrimSpace(s) != "" {
		return splitCommandArgs(s)
	}
	if arr, ok := args[0].([]any); ok {
		return extractStringSlice(arr)
	}
	if m, ok := args[0].(map[string]any); ok {
		var out []string
		if optBool(m, "stat", false) {
			out = append(out, "--stat")
		}
		if optBool(m, "staged", optBool(m, "cached", false)) {
			out = append(out, "--staged")
		}
		if n := optInt(m, "limit", optInt(m, "maxCount", 0)); n > 0 {
			out = append(out, fmt.Sprintf("-n%d", n))
		}
		if p := firstString(m, "path", "file"); p != "" {
			out = append(out, "--", p)
		}
		return out
	}
	return nil
}

func splitCommandArgs(s string) []string {
	var args []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case (c == ' ' || c == '\t') && !inSingle && !inDouble:
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}
