// Command agy-ptc provides the Programmatic Tool Calling (Code Mode) MCP server,
// SessionStart hook, TUI statusline, audit log viewer, and plugin installer for Antigravity CLI.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/aliothmoon/agy-ptc/internal/bench"
	"github.com/aliothmoon/agy-ptc/internal/snapshot"
	"github.com/aliothmoon/agy-ptc/internal/version"
	"github.com/aliothmoon/mcplex/pkg/audit"
	"github.com/aliothmoon/mcplex/pkg/mcpserver"
	ptcruntime "github.com/aliothmoon/mcplex/pkg/runtime"
)

const pluginName = "agy-ptc"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	sub := os.Args[1]
	args := os.Args[2:]

	switch sub {
	case "mcp":
		runMCP(args)
	case "hook":
		runHook(args)
	case "statusline":
		runStatusLine(args)
	case "run":
		runStandalone(args)
	case "logs":
		runLogs(args, false)
	case "errors":
		runLogs(args, true)
	case "stats":
		runStats(args)
	case "bench":
		runBench(args)
	case "install":
		if err := runInstall(args); err != nil {
			fmt.Fprintf(os.Stderr, "install error: %v\n", err)
			os.Exit(1)
		}
	case "uninstall":
		if err := runUninstall(args); err != nil {
			fmt.Fprintf(os.Stderr, "uninstall error: %v\n", err)
			os.Exit(1)
		}
	case "doctor", "status":
		if err := runDoctor(args); err != nil {
			fmt.Fprintf(os.Stderr, "doctor failed: %v\n", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		printUsage()
	case "-v", "--version", "version":
		fmt.Printf("agy-ptc v%s (%s/%s)\n", version.Version, runtime.GOOS, runtime.GOARCH)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand '%s'\n\n", sub)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`agy-ptc — Programmatic Tool Calling (Code Mode) Runtime & Plugin for Antigravity CLI

Usage:
  agy-ptc <command> [flags]

Commands:
  mcp [--data-dir <dir>]                 Start the resident Stdio MCP server
  hook session-start                     Run the fast SessionStart workspace snapshot hook
  statusline [--data-dir <dir>]          Render live PTC TUI status line for Antigravity CLI
  run [--cwd <dir>] [--code <ts> | file] Execute a TypeScript PTC program directly from CLI
  logs [-f] [-e] [--full] [--id <id>]    Inspect or live-tail (-f) PTC input code & return output
  errors [--tail <N>] [--id <ptc_id>]    Shortcut for 'logs --errors' (errors & caught op failures)
  stats                                  Summarize PTC error categories & failing capability hotspots
  bench [--recent <N> | <file>]          Analyze local Antigravity transcript.jsonl efficiency
  install [--project <dir>]              Install & validate plugin into Antigravity CLI
  doctor [--plugin-dir <dir>]            Run live MCP handshake, hook latency & strict validation
  uninstall [--project <dir>]            Remove installed agy-ptc plugin
  version                                Print version`)
}

func runMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	dataDir := fs.String("data-dir", "", "Persistent plugin data directory")
	cwd := fs.String("cwd", "", "Default workspace directory")
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	eng := ptcruntime.NewEngine(*dataDir, *cwd)
	defer eng.Close()

	srv := mcpserver.NewServerWithOptions(eng, mcpserver.ServerOptions{
		ServerName:    pluginName,
		ServerVersion: version.Version,
	})
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "mcp server error: %v\n", err)
		os.Exit(1)
	}
}

func runHook(args []string) {
	event := "session-start"
	if len(args) > 0 {
		event = strings.ToLower(args[0])
	}
	switch event {
	case "session-start", "sessionstart":
		snapshot.RunSessionStartHook(os.Stdin, os.Stdout)
	default:
		_, _ = os.Stdout.Write([]byte("{}\n"))
	}
}

func runStatusLine(args []string) {
	fs := flag.NewFlagSet("statusline", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultCacheDataDir(), "Plugin data directory")
	_ = fs.Parse(args)
	snapshot.RunStatusLine(os.Stdin, os.Stdout, *dataDir)
}

func runStandalone(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cwd := fs.String("cwd", "", "Workspace directory")
	codeFlag := fs.String("code", "", "Inline TypeScript code to execute")
	timeoutMs := fs.Int("timeout", 20000, "Timeout in milliseconds (default 20000, max 120000)")
	maxOut := fs.Int("max-output", 65536, "Max output UTF-8 bytes in standalone CLI mode (default 65536)")
	_ = fs.Parse(args)

	code := *codeFlag
	if code == "" && fs.NArg() > 0 {
		raw, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed reading %s: %v\n", fs.Arg(0), err)
			os.Exit(1)
		}
		code = string(raw)
	}
	if code == "" {
		raw, _ := io.ReadAll(os.Stdin)
		code = string(raw)
	}
	if strings.TrimSpace(code) == "" {
		fmt.Fprintln(os.Stderr, "error: provide --code '...' or a .ts script file")
		os.Exit(1)
	}

	eng := ptcruntime.NewEngine(defaultCacheDataDir(), *cwd)
	defer eng.Close()

	out, isErr := eng.RunMCPExec(context.Background(), ptcruntime.ExecInput{
		Code:           code,
		Cwd:            *cwd,
		Title:          "cli-run",
		TimeoutMs:      *timeoutMs,
		MaxOutputBytes: *maxOut,
	})
	fmt.Println(out)
	if isErr {
		os.Exit(1)
	}
}

func runBench(args []string) {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	recent := fs.Int("recent", 15, "Number of most recent local sessions to analyze")
	_ = fs.Parse(args)

	if fs.NArg() > 0 {
		rep, err := bench.AnalyzeFile(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "bench error: %v\n", err)
			os.Exit(1)
		}
		raw, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(raw))
		return
	}
	report, err := bench.AnalyzeLocal(*recent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(report)
}

func runLogs(args []string, forceErrors bool) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	errorsOnly := fs.Bool("errors", forceErrors, "Show only runs with uncaught or caught capability errors")
	shortErr := fs.Bool("e", forceErrors, "Alias for --errors")
	follow := fs.Bool("f", false, "Live-stream incoming MCP PTC calls (input code, ops, and return output)")
	followLong := fs.Bool("follow", false, "Alias for -f")
	full := fs.Bool("full", false, "Print input TypeScript code and return output for each entry")
	tail := fs.Int("tail", 20, "Number of recent entries to display")
	runID := fs.String("id", "", "Inspect full TypeScript source and trace for a specific run ID")
	dataDir := fs.String("data-dir", "", "Explicit plugin data directory")
	asJSON := fs.Bool("json", false, "Output raw JSON array")
	clearLogs := fs.Bool("clear", false, "Delete recorded execution/error JSONL logs")
	_ = fs.Parse(args)

	if *runID == "" && fs.NArg() > 0 {
		*runID = fs.Arg(0)
	}
	if *clearLogs {
		dirs := audit.DiscoverLogDirs(*dataDir)
		for _, d := range dirs {
			_ = os.Remove(filepath.Join(d, "executions.jsonl"))
			_ = os.Remove(filepath.Join(d, "errors.jsonl"))
		}
		fmt.Printf("Cleared PTC logs across %d directory(s).\n", len(dirs))
		return
	}

	wantErrors := *errorsOnly || *shortErr
	if *follow || *followLong {
		streamLogsLive(*dataDir, wantErrors, *tail)
		return
	}
	if *runID != "" {
		wantErrors = false
	}
	entries, files, err := audit.LoadEntries(*dataDir, wantErrors)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logs error: %v\n", err)
		os.Exit(1)
	}
	if *asJSON {
		raw, _ := json.MarshalIndent(entries, "", "  ")
		fmt.Println(string(raw))
		return
	}
	if *full && *runID == "" {
		start := 0
		if *tail > 0 && len(entries) > *tail {
			start = len(entries) - *tail
		}
		for _, e := range entries[start:] {
			fmt.Print(audit.FormatFullStreamCard(e))
		}
		return
	}
	fmt.Print(audit.FormatLogs(entries, files, *tail, *runID))
}

func streamLogsLive(explicitDataDir string, errorsOnly bool, initialTail int) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fileName := "executions.jsonl"
	if errorsOnly {
		fileName = "errors.jsonl"
	}

	entries, files, _ := audit.LoadEntries(explicitDataDir, errorsOnly)
	start := max(0, len(entries)-min(initialTail, 5))
	fmt.Printf("Watching AgyPTC live MCP calls (Ctrl+C to stop)...\n")
	for _, e := range entries[start:] {
		fmt.Print(audit.FormatFullStreamCard(e))
	}

	// Record current byte offsets so subsequent ticks read only newly appended bytes (O(delta)).
	offsets := make(map[string]int64, len(files))
	for _, p := range files {
		if fi, err := os.Stat(p); err == nil {
			offsets[p] = fi.Size()
		}
	}

	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, dir := range audit.DiscoverLogDirs(explicitDataDir) {
				p := filepath.Join(dir, fileName)
				fi, err := os.Stat(p)
				if err != nil {
					continue
				}
				off := offsets[p]
				if fi.Size() < off {
					off = 0 // log rotated or cleared
				}
				if fi.Size() == off {
					continue
				}
				newEntries, nextOff := readJSONLDelta(p, off)
				offsets[p] = nextOff
				for _, e := range newEntries {
					fmt.Print(audit.FormatFullStreamCard(e))
				}
			}
		}
	}
}

func readJSONLDelta(path string, offset int64) ([]audit.Entry, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset
	}
	r := bufio.NewReaderSize(f, 64*1024)
	var out []audit.Entry
	cur := offset
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			cur += int64(len(line))
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				var e audit.Entry
				if json.Unmarshal(trimmed, &e) == nil {
					out = append(out, e)
				}
			}
		}
		if err != nil {
			break
		}
	}
	return out, cur
}

func runStats(args []string) {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	dataDir := fs.String("data-dir", "", "Explicit plugin data directory")
	_ = fs.Parse(args)

	entries, _, err := audit.LoadEntries(*dataDir, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stats error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(audit.FormatStats(entries))
}

func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	projectDir := fs.String("project", "", "Install into <project>/.agents/plugins/agy-ptc instead of global")
	srcPluginDir := fs.String("source", "", "Source plugin folder (auto-detected if omitted)")
	noEnable := fs.Bool("no-enable", false, "Skip running 'agy plugin enable agy-ptc'")
	if err := fs.Parse(args); err != nil {
		return err
	}

	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	exePath, _ = filepath.EvalSymlinks(exePath)

	srcDir := *srcPluginDir
	if srcDir == "" {
		srcDir = locateSourcePluginDir(exePath)
	}
	if srcDir == "" {
		return errors.New("cannot locate plugin/ template folder; pass --source <path/to/plugin>")
	}

	var targetDir string
	if *projectDir != "" {
		absProj, err := filepath.Abs(*projectDir)
		if err != nil {
			return err
		}
		targetDir = filepath.Join(absProj, ".agents", "plugins", pluginName)
	} else {
		targetDir = filepath.Join(userHomeDir(), ".gemini", "config", "plugins", pluginName)
	}

	if filepath.Clean(srcDir) != filepath.Clean(targetDir) {
		if err := copyDirClean(srcDir, targetDir); err != nil {
			return fmt.Errorf("copying plugin files: %w", err)
		}
	}

	// Copy current agy-ptc binary into <targetDir>/scripts/agy-ptc(.exe) and standard user Go/local bin dir
	binName := "agy-ptc"
	if runtime.GOOS == "windows" {
		binName = "agy-ptc.exe"
	}
	rawBin, err := os.ReadFile(exePath)
	if err != nil {
		return err
	}
	dstBin := filepath.Join(targetDir, "scripts", binName)
	if filepath.Clean(exePath) != filepath.Clean(dstBin) {
		if err := os.MkdirAll(filepath.Dir(dstBin), 0o755); err != nil {
			return err
		}
		if err := writeExecutableSafe(dstBin, rawBin); err != nil {
			return err
		}
	}
	if userBinDir := detectUserBinDir(); userBinDir != "" {
		pathBin := filepath.Join(userBinDir, binName)
		if filepath.Clean(exePath) != filepath.Clean(pathBin) {
			_ = os.MkdirAll(userBinDir, 0o755)
			if err := writeExecutableSafe(pathBin, rawBin); err == nil {
				fmt.Printf("Installed CLI binary to %s\n", pathBin)
			}
		}
	}

	fmt.Printf("Installed %s to %s\n", pluginName, targetDir)

	if *projectDir == "" && !*noEnable {
		if agyBin, err := exec.LookPath("agy"); err == nil {
			cmd := exec.Command(agyBin, "plugin", "enable", pluginName)
			if out, err := cmd.CombinedOutput(); err == nil {
				fmt.Printf("Enabled '%s' via agy CLI.\n", pluginName)
			} else {
				fmt.Printf("Note: 'agy plugin enable %s' output: %s\n", pluginName, strings.TrimSpace(string(out)))
			}
		}
	}

	return runDoctor([]string{"--plugin-dir", targetDir})
}

func runUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	projectDir := fs.String("project", "", "Uninstall from <project>/.agents/plugins/agy-ptc")
	purge := fs.Bool("purge", false, "Also remove user CLI binary and plugin_data (logs/state)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var targetDir string
	if *projectDir != "" {
		absProj, _ := filepath.Abs(*projectDir)
		targetDir = filepath.Join(absProj, ".agents", "plugins", pluginName)
	} else {
		targetDir = filepath.Join(userHomeDir(), ".gemini", "config", "plugins", pluginName)
		if agyBin, err := exec.LookPath("agy"); err == nil {
			_ = exec.Command(agyBin, "plugin", "disable", pluginName).Run()
		}
	}

	if _, err := os.Stat(targetDir); os.IsNotExist(err) {
		fmt.Printf("Plugin directory not found (%s).\n", targetDir)
	} else {
		if err := os.RemoveAll(targetDir); err != nil {
			return err
		}
		fmt.Printf("Removed %s from %s\n", pluginName, targetDir)
	}

	if *projectDir == "" && *purge {
		binName := "agy-ptc"
		if runtime.GOOS == "windows" {
			binName = "agy-ptc.exe"
		}
		if userBinDir := detectUserBinDir(); userBinDir != "" {
			pathBin := filepath.Join(userBinDir, binName)
			if err := os.Remove(pathBin); err == nil {
				fmt.Printf("Removed CLI binary %s\n", pathBin)
			}
		}
		h := userHomeDir()
		for _, d := range []string{
			filepath.Join(h, ".gemini", "antigravity-cli", "plugin_data", pluginName),
			filepath.Join(h, ".gemini", "antigravity", "plugin_data", pluginName),
		} {
			if err := os.RemoveAll(d); err == nil {
				fmt.Printf("Removed plugin data %s\n", d)
			}
		}
	}
	return nil
}

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	pluginDir := fs.String("plugin-dir", "", "Plugin directory to verify")
	if err := fs.Parse(args); err != nil {
		return err
	}

	target := *pluginDir
	if target == "" {
		exePath, _ := os.Executable()
		if src := locateSourcePluginDir(exePath); src != "" {
			target = src
		} else {
			target = filepath.Join(userHomeDir(), ".gemini", "config", "plugins", pluginName)
		}
	}

	exePath, err := os.Executable()
	if err != nil {
		return err
	}

	fmt.Println("agy-ptc health check:")
	fmt.Printf("  - Binary      : %s (v%s, %s/%s)\n", exePath, version.Version, runtime.GOOS, runtime.GOARCH)
	fmt.Printf("  - Plugin dir  : %s\n", target)
	if rgPath, err := exec.LookPath("rg"); err == nil {
		fmt.Printf("  - ripgrep (rg): available (%s)\n", rgPath)
	} else {
		fmt.Println("  - ripgrep (rg): not found (pure-Go fallback active)")
	}

	// 1. Live MCP handshake + PTC execution smoke check
	mcpStart := time.Now()
	if err := smokeTestMCP(exePath, target); err != nil {
		return fmt.Errorf("MCP live check failed: %w", err)
	}
	fmt.Printf("  - MCP + PTC VM: OK (%dms handshake + TypeScript sandbox round-trip)\n", time.Since(mcpStart).Milliseconds())

	// 2. Live SessionStart hook latency check
	hookStart := time.Now()
	hookBytes, err := smokeTestHook(exePath, target)
	if err != nil {
		return fmt.Errorf("SessionStart hook check failed: %w", err)
	}
	fmt.Printf("  - SessionStart: OK (%dms, %d UTF-8 bytes)\n", time.Since(hookStart).Milliseconds(), hookBytes)

	// 3. `agy plugin validate --strict` if `agy` is installed and plugin.json exists
	if _, statErr := os.Stat(filepath.Join(target, "plugin.json")); statErr == nil {
		if agyBin, lookErr := exec.LookPath("agy"); lookErr == nil {
			valCmd := exec.Command(agyBin, "plugin", "validate", "--strict", target)
			valOut, valErr := valCmd.CombinedOutput()
			if valErr != nil {
				return fmt.Errorf("agy plugin validate --strict failed:\n%s", strings.TrimSpace(string(valOut)))
			}
			fmt.Println("  - agy validate: PASSED (--strict, 0 warnings/errors)")
		}
	}

	return nil
}

func smokeTestMCP(exePath, sampleCwd string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, exePath, "mcp")
	var stdout, stderr bytes.Buffer
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	reqs := []map[string]any{
		{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "initialize",
			"params":  map[string]any{"protocolVersion": "2025-03-26"},
		},
		{
			"jsonrpc": "2.0",
			"method":  "notifications/initialized",
		},
		{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "tools/list",
		},
		{
			"jsonrpc": "2.0",
			"id":      3,
			"method":  "tools/call",
			"params": map[string]any{
				"name": "exec",
				"arguments": map[string]any{
					"cwd":      sampleCwd,
					"code":     `const x: number = 21 * 2; return { answer: x, msg: P.hello };`,
					"payloads": map[string]string{"hello": "world"},
				},
			},
		},
	}
	for _, r := range reqs {
		b, _ := json.Marshal(r)
		_, _ = stdin.Write(append(b, '\n'))
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 3 {
		return fmt.Errorf("expected >=3 JSON-RPC responses, got %d: %s", len(lines), stdout.String())
	}
	var callResp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &callResp); err != nil {
		return fmt.Errorf("failed decoding tools/call response: %w", err)
	}
	if callResp.Result.IsError || len(callResp.Result.Content) == 0 || !strings.Contains(callResp.Result.Content[0].Text, `"answer": 42`) {
		return fmt.Errorf("unexpected PTC smoke result: %s", stdout.String())
	}
	return nil
}

func detectUserBinDir() string {
	if gobin := strings.TrimSpace(os.Getenv("GOBIN")); gobin != "" {
		return gobin
	}
	if goExe, err := exec.LookPath("go"); err == nil {
		if out, err := exec.Command(goExe, "env", "GOBIN").Output(); err == nil {
			if gb := strings.TrimSpace(string(out)); gb != "" {
				return gb
			}
		}
		if out, err := exec.Command(goExe, "env", "GOPATH").Output(); err == nil {
			if gp := strings.TrimSpace(string(out)); gp != "" {
				first := strings.Split(gp, string(os.PathListSeparator))[0]
				if first != "" {
					return filepath.Join(first, "bin")
				}
			}
		}
	}
	if gopath := strings.TrimSpace(os.Getenv("GOPATH")); gopath != "" {
		first := strings.Split(gopath, string(os.PathListSeparator))[0]
		if first != "" {
			return filepath.Join(first, "bin")
		}
	}
	if h := userHomeDir(); h != "" {
		goBin := filepath.Join(h, "go", "bin")
		localBin := filepath.Join(h, ".local", "bin")
		pathEntries := filepath.SplitList(os.Getenv("PATH"))
		for _, pe := range pathEntries {
			if strings.EqualFold(filepath.Clean(pe), filepath.Clean(goBin)) {
				return goBin
			}
			if strings.EqualFold(filepath.Clean(pe), filepath.Clean(localBin)) {
				return localBin
			}
		}
		return goBin
	}
	return ""
}

func smokeTestHook(exePath, sampleCwd string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exePath, "hook", "session-start")
	cmd.Env = append(os.Environ(), "AGY_PTC_DRY_RUN_HOOK=1")
	inJSON, _ := json.Marshal(map[string]any{
		"cwd":           sampleCwd,
		"hookEventName": "SessionStart",
	})
	cmd.Stdin = bytes.NewReader(inJSON)
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	var parsed struct {
		InjectSteps []struct {
			SystemMessage struct {
				SystemMessage string `json:"systemMessage"`
			} `json:"systemMessage"`
		} `json:"injectSteps"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &parsed); err != nil {
		return 0, err
	}
	if len(parsed.InjectSteps) == 0 || parsed.InjectSteps[0].SystemMessage.SystemMessage == "" {
		return 0, errors.New("SessionStart returned empty snapshot")
	}
	return len(parsed.InjectSteps[0].SystemMessage.SystemMessage), nil
}

func locateSourcePluginDir(exePath string) string {
	exeDir := filepath.Dir(exePath)
	candidates := []string{
		filepath.Join(exeDir, "plugin"),
		filepath.Join(exeDir, ".."), // when binary is inside <plugin>/scripts/agy-ptc
		filepath.Join(exeDir, "..", "plugin"),
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "plugin"), cwd)
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "plugin.json")); err == nil {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return ""
}

func copyDirClean(src, dst string) error {
	// Clean up any previous .old Windows hot-swap binaries if unlocked
	_ = filepath.Walk(dst, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".old") {
			_ = os.Remove(p)
		}
		return nil
	})

	expected := map[string]bool{
		".":                   true,
		"scripts":             true,
		"scripts/agy-ptc":     true,
		"scripts/agy-ptc.exe": true,
	}

	if err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		expected[relSlash] = true
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if strings.HasPrefix(relSlash, "scripts/") {
			mode = 0o755
		}
		if strings.HasSuffix(strings.ToLower(target), ".exe") {
			return writeExecutableSafe(target, data)
		}
		return writeFileAtomic(target, data, mode)
	}); err != nil {
		return err
	}

	// Remove stale files and empty directories left over from earlier plugin versions.
	var staleDirs []string
	_ = filepath.Walk(dst, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(dst, p)
		if err != nil || rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if info.IsDir() {
			if !expected[relSlash] {
				staleDirs = append(staleDirs, p)
			}
			return nil
		}
		if strings.HasSuffix(relSlash, ".old") {
			return nil
		}
		if !expected[relSlash] {
			_ = os.Remove(p)
		}
		return nil
	})
	for i := len(staleDirs) - 1; i >= 0; i-- {
		_ = os.Remove(staleDirs[i])
	}
	return nil
}

func writeFileAtomic(dstPath string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".agy-ptc-tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmpPath, mode)
	if err := os.Rename(tmpPath, dstPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// writeExecutableSafe atomically installs or replaces an executable without a truncation window,
// and restores the previous binary if Windows hot-swap replacement fails mid-way.
func writeExecutableSafe(dstPath string, data []byte) error {
	dir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".agy-ptc-bin-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil && runtime.GOOS != "windows" {
		return err
	}

	// Fast path: atomic direct rename works on Unix and when dstPath is absent/replaceable.
	if err := os.Rename(tmpPath, dstPath); err == nil {
		cleanupTmp = false
		return nil
	}

	// Fallback for Windows locked executables: move existing binary aside, rename new one in,
	// and roll back to the previous binary if the second rename fails.
	if _, statErr := os.Stat(dstPath); statErr != nil {
		return fmt.Errorf("installing executable %s: %w", dstPath, statErr)
	}
	oldPath := fmt.Sprintf("%s.%d.old", dstPath, time.Now().UnixNano())
	if err := os.Rename(dstPath, oldPath); err != nil {
		return fmt.Errorf("moving existing executable aside: %w", err)
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		_ = os.Rename(oldPath, dstPath)
		return fmt.Errorf("replacing executable %s: %w", dstPath, err)
	}
	cleanupTmp = false
	_ = os.Remove(oldPath) // best-effort cleanup if not locked by a running process
	return nil
}

func defaultCacheDataDir() string {
	if pd := strings.TrimSpace(os.Getenv("PLUGIN_DATA")); pd != "" {
		return pd
	}
	h := userHomeDir()
	candidates := []string{
		filepath.Join(h, ".gemini", "antigravity-cli", "plugin_data", pluginName),
		filepath.Join(h, ".gemini", "antigravity", "plugin_data", pluginName),
		filepath.Join(h, ".gemini", "config", "plugin_data", pluginName),
	}
	for _, dir := range candidates {
		if fi, err := os.Stat(filepath.Join(dir, "logs")); err == nil && fi.IsDir() {
			return dir
		}
	}
	for _, dir := range candidates {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
	}
	return filepath.Join(h, ".gemini", "cache", pluginName)
}

func userHomeDir() string {
	if runtime.GOOS == "windows" {
		if up := os.Getenv("USERPROFILE"); up != "" {
			return up
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}
