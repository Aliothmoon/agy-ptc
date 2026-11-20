package capabilities

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
			extraEnv, _ := opts["env"].(map[string]any)
			res, err := b.runShell(ctx, workDir, cmdStr, cmdTimeout, settle, compact, firstString(opts, "shell"), extraEnv)
			if err != nil {
				return nil, err
			}
			return res, nil
		},
		// sh.run(file, args?, opts?) runs a program directly (no shell) -> { ok, exitCode, timedOut, stdout, stderr, durationMs }.
		"run": func(ctx context.Context, args []any) (any, error) {
			file, argv, opts, err := parseRunArgs(args)
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
			return b.RunArgv(ctx, env, workDir, file, argv, opts)
		},
		"checks": func(ctx context.Context, args []any) (any, error) {
			return b.RunChecksBatch(ctx, env.Cwd, args)
		},
	}

	if err := env.SetNamespace("sh", shAsync, nil); err != nil {
		return err
	}
	// sh.shell tells the program which dialect sh.exec speaks by default ("pwsh" | "cmd" | "sh").
	if shObj := env.VM.Get("sh"); shObj != nil {
		_ = shObj.ToObject(env.VM).Set("shell", defaultShellName())
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
	return b.runShell(parentCtx, cwd, cmdLine, timeoutDur, settle, compact, "", nil)
}

// runShell runs cmdLine through `shell` ("" = platform default) with optional extra environment variables.
func (b *ShellGitBinder) runShell(parentCtx context.Context, cwd, cmdLine string, timeoutDur time.Duration, settle, compact bool, shell string, extraEnv map[string]any) (map[string]any, error) {
	if err := b.Policy.CheckCommand(cmdLine); err != nil {
		return nil, err
	}
	shellExe, shellArgs, err := resolveShell(shell)
	if err != nil {
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
	cmd := exec.CommandContext(ctx, shellExe, append(shellArgs, cmdLine)...)
	cmd.Dir = cwd
	setSysProcAttrHide(cmd)
	if len(extraEnv) > 0 {
		cmd.Env = mergeEnv(extraEnv)
	}

	stdout := newLimitedSyncBuffer(512 * 1024)
	stderr := newLimitedSyncBuffer(256 * 1024)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	durMs := time.Since(start).Milliseconds()
	parentErr := parentCtx.Err()
	parentTimedOut := errors.Is(parentErr, context.DeadlineExceeded)
	parentAborted := parentErr != nil && !parentTimedOut
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(runErr, exec.ErrWaitDelay) || parentTimedOut
	exitCode := 0
	spawnErr := ""

	if runErr != nil {
		var exitErr *exec.ExitError
		if timedOut || parentAborted {
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
	if compact || timedOut || parentErr != nil {
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
	} else if parentAborted {
		msg := strings.TrimSpace(formatted)
		if msg == "" || msg == "(no output)" {
			formatted = fmt.Sprintf("CANCELED after %dms (outer execution already ended)", durMs)
		} else {
			formatted = fmt.Sprintf("CANCELED after %dms (outer execution already ended)\n%s", durMs, msg)
		}
	}

	// If the outer vm_exec deadline or cancellation fired, return an error so the sandbox trace captures the partial stdout/stderr!
	if parentErr != nil {
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
		kind := sandbox.KindCmdFailed
		if timedOut {
			kind = sandbox.KindTimeout
		}
		return nil, sandbox.NewHostError(kind, map[string]any{"exitCode": exitCode}, "`%s` exit %d: %s", cmdLine, exitCode, output.TruncateWithByteNotice(formatted, 4000))
	}
	return envelope, nil
}

const maxChecksPerCall = 12

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
	if len(rawList) > maxChecksPerCall {
		// Never drop checks silently: a truncated verify list could let a tx commit with unrun checks.
		return nil, sandbox.NewHostError(sandbox.KindBadArgs, nil, "max %d commands per call (got %d); split into batches", maxChecksPerCall, len(rawList))
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

// Read-only option allowlists for git subcommands that ALSO have mutating forms (`git branch -D`, `git tag -d`,
// `git remote add`). Values map to whether the long option consumes the following argument when no `=` is present.
var (
	gitBranchListOpts = map[string]bool{
		"--all": false, "--remotes": false, "--verbose": false, "--list": false, "--show-current": false,
		"--ignore-case": false, "--no-color": false, "--no-column": false, "--no-abbrev": false, "--omit-empty": false,
		"--quiet": false, "--color": false, "--column": false, "--abbrev": false,
		"--contains": true, "--no-contains": true, "--merged": true, "--no-merged": true, "--points-at": true,
		"--sort": true, "--format": true,
	}
	gitBranchListShort = "arvliq"
	gitTagListOpts     = map[string]bool{
		"--list": false, "--ignore-case": false, "--no-color": false, "--no-column": false, "--omit-empty": false,
		"--color": false, "--column": false,
		"--contains": true, "--no-contains": true, "--merged": true, "--no-merged": true, "--points-at": true,
		"--sort": true, "--format": true,
	}
	gitTagListShort = "li"
)

// validateReadOnlyGitArgs rejects any git invocation that could mutate the repository or execute helpers.
func validateReadOnlyGitArgs(gitArgs []string) error {
	if len(gitArgs) == 0 {
		return errors.New("git subcommand required")
	}
	// Disallow global git flags like -c core.pager=... or --exec-path before the subcommand
	sub := gitArgs[0]
	if strings.HasPrefix(sub, "-") || !allowedGitReadSubcommands[sub] {
		return fmt.Errorf("git subcommand '%s' is not in the read-only allowlist (use sh.exec for write git operations)", sub)
	}
	rest := gitArgs[1:]
	for _, a := range rest {
		if strings.HasPrefix(a, "--upload-pack") || strings.HasPrefix(a, "--exec") || strings.HasPrefix(a, "--output") ||
			strings.HasPrefix(a, "--ext-diff") || strings.HasPrefix(a, "--open-files-in-pager") {
			return fmt.Errorf("disallowed git flag '%s' in read-only git query", a)
		}
	}
	switch sub {
	case "branch":
		return validateListOnlyArgs("branch", rest, gitBranchListOpts, gitBranchListShort, "-l", "--list")
	case "tag":
		// `-n<num>` (annotation lines) is list-only too.
		filtered := make([]string, 0, len(rest))
		for _, a := range rest {
			if len(a) >= 2 && strings.HasPrefix(a, "-n") && strings.Trim(a[2:], "0123456789") == "" {
				continue
			}
			filtered = append(filtered, a)
		}
		return validateListOnlyArgs("tag", filtered, gitTagListOpts, gitTagListShort, "-l", "--list")
	case "remote":
		i := 0
		for i < len(rest) && (rest[i] == "-v" || rest[i] == "--verbose") {
			i++
		}
		if i == len(rest) || rest[i] == "show" || rest[i] == "get-url" {
			return nil
		}
		return fmt.Errorf("only read-only 'git remote [-v]', 'git remote show' and 'git remote get-url' are allowed in git.* (got '%s')", rest[i])
	}
	return nil
}

// validateListOnlyArgs accepts only listing options; positional arguments (patterns) require explicit list mode,
// because `git branch <name>` / `git tag <name>` CREATE refs.
func validateListOnlyArgs(sub string, args []string, longOpts map[string]bool, shortLetters string, listFlags ...string) error {
	listMode := false
	var positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			positionals = append(positionals, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "--"):
			name, _, hasValue := strings.Cut(a, "=")
			takesValue, ok := longOpts[name]
			if !ok {
				return fmt.Errorf("git %s option '%s' is not allowed in read-only git.* (use sh.exec for ref changes)", sub, a)
			}
			for _, lf := range listFlags {
				if name == lf {
					listMode = true
				}
			}
			if takesValue && !hasValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for _, c := range a[1:] {
				if !strings.ContainsRune(shortLetters, c) {
					return fmt.Errorf("git %s flag '-%c' is not allowed in read-only git.* (use sh.exec for ref changes)", sub, c)
				}
				if c == 'l' {
					listMode = true
				}
			}
		default:
			positionals = append(positionals, a)
		}
	}
	if len(positionals) > 0 && !listMode {
		return fmt.Errorf("'git %s %s' would create a ref; pass '--list' to filter by pattern in read-only git.*", sub, strings.Join(positionals, " "))
	}
	return nil
}

func runReadOnlyGit(ctx context.Context, cwd string, gitArgs []string) (string, error) {
	if err := validateReadOnlyGitArgs(gitArgs); err != nil {
		return "", sandbox.NewHostError(sandbox.KindBadArgs, nil, "%v", err)
	}
	sub := gitArgs[0]

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
	staged, modified, untracked := []string{}, []string{}, []string{}
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
