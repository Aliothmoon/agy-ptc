package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPTCAgentManifestUsesOnlyRegisteredStaticTools(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "plugin", "agents", "ptc.md"))
	if err != nil {
		t.Fatalf("read ptc.md: %v", err)
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		t.Fatalf("expected YAML frontmatter at top of ptc.md")
	}
	rest := content[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		t.Fatalf("unterminated YAML frontmatter in ptc.md")
	}
	front := rest[:end]

	allowedStaticTools := map[string]bool{
		"read_file":        true,
		"write_file":       true,
		"edit_file":        true,
		"run_command":      true,
		"manage_task":      true,
		"search_web":       true,
		"ask_question":     true,
		"define_subagent":  true,
		"invoke_subagent":  true,
		"manage_subagents": true,
		"send_message":     true,
		"run_workflow":     true,
		"schedule":         true,
	}
	forbiddenDynamicTools := map[string]bool{
		"swarm":          true,
		"call_mcp_tool":  true,
		"list_resources": true,
		"read_resource":  true,
	}

	inTools := false
	foundTools := 0
	for _, line := range strings.Split(front, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "tools:" {
			inTools = true
			continue
		}
		if inTools {
			if strings.HasPrefix(trimmed, "- ") {
				toolName := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
				foundTools++
				if forbiddenDynamicTools[toolName] {
					t.Errorf("ptc.md lists non-registry dynamic tool %q (breaks invoke_subagent)", toolName)
				}
				if !allowedStaticTools[toolName] {
					t.Errorf("ptc.md lists unknown static tool %q", toolName)
				}
			} else if trimmed != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				inTools = false
			}
		}
	}
	if foundTools == 0 {
		t.Fatalf("expected non-empty tools list in ptc.md")
	}
	if !strings.Contains(front, "inheritMcp: true") {
		t.Errorf("expected inheritMcp: true in ptc.md")
	}
}

func TestLocateSourcePluginDirRejectsForeignPluginManifest(t *testing.T) {
	tmp := t.TempDir()
	foreignDir := filepath.Join(tmp, "other-repo")
	if err := os.MkdirAll(foreignDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreignDir, "plugin.json"), []byte(`{"name":"some-other-plugin"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origWd) }()
	if err := os.Chdir(foreignDir); err != nil {
		t.Fatal(err)
	}

	fakeExe := filepath.Join(tmp, "gobin", "agy-ptc")
	if got := locateSourcePluginDir(fakeExe); got != "" {
		t.Fatalf("expected empty plugin dir when cwd belongs to foreign plugin, got %q", got)
	}

	validPlugin := filepath.Join(foreignDir, "plugin")
	if err := os.MkdirAll(validPlugin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(validPlugin, "plugin.json"), []byte(`{"name":"agy-ptc"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := locateSourcePluginDir(fakeExe)
	if filepath.Clean(got) != filepath.Clean(validPlugin) {
		t.Fatalf("expected %q, got %q", validPlugin, got)
	}
}

func TestComputeLiveTailStartBounds(t *testing.T) {
	cases := []struct {
		total int
		tail  int
		want  int
	}{
		{total: 10, tail: -1, want: 10},
		{total: 10, tail: 0, want: 10},
		{total: 10, tail: 5, want: 5},
		{total: 10, tail: 15, want: 0},
		{total: 0, tail: -5, want: 0},
	}
	for _, tc := range cases {
		got := computeLiveTailStart(tc.total, tc.tail)
		if got < 0 || got > tc.total {
			t.Fatalf("computeLiveTailStart(%d, %d) = %d (out of slice bounds [0,%d])", tc.total, tc.tail, got, tc.total)
		}
		if got != tc.want {
			t.Fatalf("computeLiveTailStart(%d, %d) = %d, want %d", tc.total, tc.tail, got, tc.want)
		}
	}
}

func TestAgyPTCBinaryDoesNotDependOnTUI(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	deps := string(out)
	for _, forbidden := range []string{
		"github.com/aliothmoon/mcplex/pkg/tui",
		"github.com/charmbracelet/bubbletea",
		"github.com/charmbracelet/lipgloss",
	} {
		if strings.Contains(deps, forbidden) {
			t.Fatalf("cmd/agy-ptc must remain headless/zero-TUI, but depends on %s", forbidden)
		}
	}
}

func TestLaunchMjsSupervisorHotReloadAndCrashRecovery(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	tmp := t.TempDir()
	binExt := ""
	if runtime.GOOS == "windows" {
		binExt = ".exe"
	}
	testBin := filepath.Join(tmp, "agy-ptc"+binExt)
	buildCmd := exec.Command("go", "build", "-o", testBin, ".")
	if out, bErr := buildCmd.CombinedOutput(); bErr != nil {
		t.Fatalf("go build test binary failed: %v\n%s", bErr, out)
	}

	launchScript, err := filepath.Abs(filepath.Join("..", "..", "plugin", "scripts", "launch.mjs"))
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(nodeBin, launchScript, "mcp", "--data-dir", filepath.Join(tmp, "data"))
	cmd.Env = append(os.Environ(), "AGY_PTC_BIN="+testBin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
		// Ensure all hot-reloaded Windows worker processes have released testBin before TempDir cleanup.
		for i := 0; i < 20; i++ {
			if err := os.Remove(testBin); err == nil || os.IsNotExist(err) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	reader := bufio.NewReader(stdout)
	sendAndRead := func(payload string) string {
		t.Helper()
		if _, wErr := io.WriteString(stdin, payload+"\n"); wErr != nil {
			t.Fatalf("write stdin: %v", wErr)
		}
		line, rErr := reader.ReadString('\n')
		if rErr != nil {
			t.Fatalf("read stdout: %v", rErr)
		}
		return line
	}

	// 1. Handshake + initial exec call
	initResp := sendAndRead(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if !strings.Contains(initResp, `"protocolVersion"`) {
		t.Fatalf("unexpected init response: %s", initResp)
	}
	_, _ = io.WriteString(stdin, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")

	wsArg, _ := json.Marshal(tmp)
	tryExtractWorkerPID := func(callID string) (int, bool, string) {
		t.Helper()
		raw := sendAndRead(`{"jsonrpc":"2.0","id":` + callID + `,"method":"tools/call","params":{"name":"exec","arguments":{"cwd":` + string(wsArg) + `,"code":"const r = await sh.run('node', ['-e', 'console.log(process.ppid)']); return r.stdout;"}}}`)
		var rpcResp struct {
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(raw), &rpcResp); err != nil || len(rpcResp.Result.Content) == 0 {
			t.Fatalf("call %s malformed response: %v (raw=%s)", callID, err, raw)
		}
		txt := rpcResp.Result.Content[0].Text
		if rpcResp.Result.IsError {
			return 0, true, txt
		}
		lines := strings.Split(strings.TrimSpace(txt), "\n")
		pid, err := strconv.Atoi(strings.TrimSpace(lines[len(lines)-1]))
		if err != nil || pid <= 0 {
			t.Fatalf("failed parsing worker PID from %q: %v", txt, err)
		}
		return pid, false, txt
	}

	pid1, isErr1, txt1 := tryExtractWorkerPID("2")
	if isErr1 {
		t.Fatalf("call 2 failed: %s", txt1)
	}

	// 2. Simulate `agy-ptc install` updating the binary mtime while idle -> supervisor hot-reloads into a new worker PID!
	future := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(testBin, future, future); err != nil {
		t.Fatal(err)
	}
	pid2, isErr2, txt2 := tryExtractWorkerPID("3")
	if isErr2 || pid2 == pid1 {
		t.Fatalf("expected supervisor to hot-reload into a new worker PID (was %d), got pid=%d err=%v txt=%s", pid1, pid2, isErr2, txt2)
	}

	// 3. Hard-kill worker pid2 -> supervisor keeps stdio pipe alive and respawns a fresh worker PID!
	if p2, findErr := os.FindProcess(pid2); findErr == nil {
		_ = p2.Kill()
	}
	time.Sleep(150 * time.Millisecond)
	pid3, isErr3, txt3 := tryExtractWorkerPID("4")
	if isErr3 {
		// If OS delivered exit slightly after call 4 wrote to stdin, call 4 cleanly reports worker restart and call 5 succeeds!
		if !strings.Contains(txt3, "restarted") {
			t.Fatalf("unexpected error on call 4 after worker kill: %s", txt3)
		}
		var isErr4 bool
		var txt4 string
		pid3, isErr4, txt4 = tryExtractWorkerPID("5")
		if isErr4 {
			t.Fatalf("retry call 5 after worker crash recovery failed: %s", txt4)
		}
	}
	if pid3 == pid2 {
		t.Fatalf("expected supervisor to respawn a new worker PID after hard crash, still got %d", pid3)
	}
}

func TestSidecarTailReadAndOpTraceStats(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	tmp := t.TempDir()
	jsonlPath := filepath.Join(tmp, "executions.jsonl")
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString(`{"id":"r_` + string(rune('a'+(i%20))) + `","timestamp":"2026-11-22T10:00:00Z","status":"ok","durationMs":50,"hostOps":6,"traces":[{"seq":1,"capability":"search.rg","status":"ok","durationMs":12}]}` + "\n")
	}
	b.WriteString(`{"id":"r_err","timestamp":"2026-11-22T10:01:00Z","status":"runtime_error","errorCategory":"edit_hunk_mismatch","durationMs":90,"hostOps":4,"failedOps":1,"traces":[{"seq":1,"capability":"t.edit","status":"error","durationMs":8,"error":"target not found"}]}` + "\n")
	if err := os.WriteFile(jsonlPath, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	serverMjs, err := filepath.Abs(filepath.Join("..", "..", "plugin", "sidecars", "ptc_panel", "server.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	script := `
import vm from 'node:vm';
import { pathToFileURL } from 'node:url';
const mod = await import(pathToFileURL(process.argv[1]).href);
const uiMod = await import(pathToFileURL(process.argv[3]).href);
const html = uiMod.buildUiHtml();
const m = /<script>([\s\S]*?)<\/script>/.exec(html);
if (!m) throw new Error('missing inline script in buildUiHtml()');
new vm.Script(m[1]);
const entries = mod.readJsonlEntries(process.argv[2], 1024);
const entriesCached = mod.readJsonlEntries(process.argv[2], 1024);
if (entries !== entriesCached) throw new Error('expected stat-memoized cache hit');
const stats = mod.computeStats(entries);
console.log(JSON.stringify({ count: entries.length, opsPerRun: stats.opsPerRun, failCap: stats.failingCapabilities[0] }));
`
	uiMjs, err := filepath.Abs(filepath.Join("..", "..", "plugin", "sidecars", "ptc_panel", "ui.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(nodeBin, "--input-type=module", "-e", script, serverMjs, jsonlPath, uiMjs)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("sidecar module check failed: %v\n%s", runErr, out)
	}
	s := string(out)
	if !strings.Contains(s, `"capability":"t.edit"`) || !strings.Contains(s, `"failed":1`) {
		t.Fatalf("expected failingCapabilities to capture t.edit trace error, got: %s", s)
	}
}
