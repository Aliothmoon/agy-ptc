package capabilities

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/alioth/agy-ptc/internal/output"
	"github.com/alioth/agy-ptc/internal/policy"
	"github.com/alioth/agy-ptc/internal/sandbox"
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
			timeoutSec := optInt(opts, "timeoutSec", optInt(opts, "timeout", 60))
			// Default settle=true (pi-fabric native style): non-zero exit codes return `{ ok: false, exitCode, output }`
			// instead of blowing up Promise.all! Callers wanting exceptions can pass `{ throwOnError: true }` or `{ settle: false }`.
			settle := optBool(opts, "settle", true)
			if optBool(opts, "throwOnError", false) {
				settle = false
			}
			compact := optBool(opts, "compact", false)
			res, err := b.RunSingleCommand(ctx, workDir, cmdStr, timeoutSec, settle, compact)
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
		"status": func(ctx context.Context, _ []any) (any, error) {
			return runGitStructuredStatus(ctx, env.Cwd)
		},
		"diff": func(ctx context.Context, args []any) (any, error) {
			extra := optionalGitTailArgs(args)
			return runReadOnlyGitSoft(ctx, env.Cwd, append([]string{"diff"}, extra...))
		},
		"log": func(ctx context.Context, args []any) (any, error) {
			extra := optionalGitTailArgs(args)
			if len(extra) == 0 {
				extra = []string{"-n", "10", "--oneline"}
			}
			return runReadOnlyGitSoft(ctx, env.Cwd, append([]string{"log"}, extra...))
		},
		"show": func(ctx context.Context, args []any) (any, error) {
			extra := optionalGitTailArgs(args)
			if len(extra) == 0 {
				extra = []string{"--stat", "HEAD"}
			}
			return runReadOnlyGitSoft(ctx, env.Cwd, append([]string{"show"}, extra...))
		},
		"branch": func(ctx context.Context, _ []any) (any, error) {
			return runReadOnlyGitSoft(ctx, env.Cwd, []string{"branch", "--list", "-v"})
		},
		"blame": func(ctx context.Context, args []any) (any, error) {
			targetPath, opts, err := parsePathAndOpts(args)
			if err != nil {
				return nil, err
			}
			if _, err := b.Policy.ResolveAndCheckRead(env.Cwd, targetPath); err != nil {
				return nil, err
			}
			gitArgs := []string{"blame"}
			sLine := optInt(opts, "startLine", 0)
			eLine := optInt(opts, "endLine", 0)
			if sLine > 0 && eLine >= sLine {
				gitArgs = append(gitArgs, fmt.Sprintf("-L%d,%d", sLine, eLine))
			}
			gitArgs = append(gitArgs, "--", targetPath)
			return runReadOnlyGitSoft(ctx, env.Cwd, gitArgs)
		},
		"query": func(ctx context.Context, args []any) (any, error) {
			rawCmd, err := requireStringArg(args, 0, "subcommand")
			if err != nil {
				return nil, err
			}
			tokens := splitCommandArgs(rawCmd)
			if len(tokens) > 0 && tokens[0] == "git" {
				tokens = tokens[1:]
			}
			return runReadOnlyGitSoft(ctx, env.Cwd, tokens)
		},
	}
	return env.SetNamespace("git", gitAsync, nil)
}

// RunSingleCommand executes one shell command after validating policy rules.
func (b *ShellGitBinder) RunSingleCommand(parentCtx context.Context, cwd, cmdLine string, timeoutSec int, settle, compact bool) (map[string]any, error) {
	if err := b.Policy.CheckCommand(cmdLine); err != nil {
		return nil, err
	}
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	if timeoutSec > 300 {
		timeoutSec = 300
	}
	ctx, cancel := context.WithTimeout(parentCtx, time.Duration(timeoutSec)*time.Second)
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

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	durMs := time.Since(start).Milliseconds()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded) && parentCtx.Err() == nil
	exitCode := 0
	spawnErr := ""

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else if timedOut {
			exitCode = -1
		} else {
			exitCode = -1
			spawnErr = runErr.Error()
		}
	}

	formatted := output.FormatCommandOutput(output.CommandFormatParams{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		TimedOut: timedOut,
		SpawnErr: spawnErr,
		MaxBytes: 6000,
		Compact:  compact,
	})

	ok := exitCode == 0 && !timedOut && spawnErr == ""
	envelope := map[string]any{
		"ok":         ok,
		"exitCode":   exitCode,
		"timedOut":   timedOut,
		"durationMs": durMs,
		"output":     formatted,
	}

	if !ok && !settle {
		return nil, fmt.Errorf("command '%s' failed (exit %d): %s", cmdLine, exitCode, formatted)
	}
	return envelope, nil
}

type checkSpec struct {
	name       string
	command    string
	cwd        string
	timeoutSec int
}

// RunChecksBatch runs multiple build/lint/test commands concurrently and returns a unified pass/fail envelope.
func (b *ShellGitBinder) RunChecksBatch(ctx context.Context, baseCwd string, args []any) (map[string]any, error) {
	if len(args) == 0 || args[0] == nil {
		return nil, errors.New("checks requires an array of commands or { commands: [...] }")
	}
	var specs []checkSpec
	concurrency := 4
	defaultTimeout := 60
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
		defaultTimeout = optInt(opts, "timeoutSec", 60)
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
				name:       fmt.Sprintf("cmd_%d", i+1),
				command:    v,
				cwd:        baseCwd,
				timeoutSec: defaultTimeout,
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
				name:       nm,
				command:    cmdStr,
				cwd:        subDir,
				timeoutSec: optInt(v, "timeoutSec", defaultTimeout),
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
			r, err := b.RunSingleCommand(ctx, s.cwd, s.command, s.timeoutSec, true, compact)
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
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s failed: %s", sub, strings.TrimSpace(stderr.String()))
	}
	return output.TruncateHeadTailBytes(strings.TrimRight(stdout.String(), "\r\n"), 64*1024, ""), nil
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
