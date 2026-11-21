package snapshot

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ptcruntime "github.com/aliothmoon/mcplex/pkg/runtime"
)

func TestSessionStartHookSkipsPluginDirAndDryRun(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("PLUGIN_DATA", dataDir)

	realWs := t.TempDir()
	if err := os.WriteFile(filepath.Join(realWs, "go.mod"), []byte("module example.com/demo\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Real workspace should record active-workspace.json
	t.Setenv("AGY_PTC_DRY_RUN_HOOK", "")
	var out bytes.Buffer
	inBytes, _ := json.Marshal(map[string]string{"cwd": realWs})
	RunSessionStartHook(bytes.NewReader(inBytes), &out)
	if !strings.Contains(out.String(), "agy-ptc workspace snapshot") {
		t.Fatalf("expected snapshot response, got: %s", out.String())
	}
	hintFile := filepath.Join(dataDir, "active-workspace.json")
	rawHint, err := os.ReadFile(hintFile)
	if err != nil {
		t.Fatalf("expected active-workspace.json to be written: %v", err)
	}
	if !strings.Contains(string(rawHint), filepath.Base(realWs)) {
		t.Fatalf("expected hint to point to realWs, got: %s", string(rawHint))
	}

	// 2. Plugin dir (contains plugin.json) MUST NOT overwrite active-workspace.json
	pluginDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"name":"agy-ptc"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	inPlugin, _ := json.Marshal(map[string]string{"cwd": pluginDir})
	RunSessionStartHook(bytes.NewReader(inPlugin), &out)
	afterPlugin, _ := os.ReadFile(hintFile)
	if string(afterPlugin) != string(rawHint) {
		t.Fatalf("plugin directory overwrote active-workspace.json: got %s, want %s", string(afterPlugin), string(rawHint))
	}

	// 2b. Another project that happens to have its own plugin.json (name != "agy-ptc") MUST still be recorded!
	otherPluginProj := t.TempDir()
	if err := os.WriteFile(filepath.Join(otherPluginProj, "plugin.json"), []byte(`{"name":"my-custom-plugin"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	inOtherPlugin, _ := json.Marshal(map[string]string{"cwd": otherPluginProj})
	RunSessionStartHook(bytes.NewReader(inOtherPlugin), &out)
	afterOtherPlugin, _ := os.ReadFile(hintFile)
	if !strings.Contains(string(afterOtherPlugin), filepath.Base(otherPluginProj)) {
		t.Fatalf("non-agy-ptc plugin project should update active-workspace.json, got: %s", string(afterOtherPlugin))
	}
	rawHint = afterOtherPlugin

	// 3. Dry-run hook (used by doctor/install) MUST NOT overwrite active-workspace.json
	otherWs := t.TempDir()
	t.Setenv("AGY_PTC_DRY_RUN_HOOK", "1")
	out.Reset()
	inOther, _ := json.Marshal(map[string]string{"cwd": otherWs})
	RunSessionStartHook(bytes.NewReader(inOther), &out)
	afterDryRun, _ := os.ReadFile(hintFile)
	if string(afterDryRun) != string(rawHint) {
		t.Fatalf("dry-run hook overwrote active-workspace.json: got %s, want %s", string(afterDryRun), string(rawHint))
	}
}

func TestStatusLineStaleActiveFallsBackToIdle(t *testing.T) {
	dataDir := t.TempDir()

	// Stale Active=true (e.g. crashed MCP server 10 minutes ago) must NOT stick as running.
	stale := ptcruntime.LiveStatus{
		Active:       true,
		Title:        "crashed-task",
		CurrentOp:    "#3 sh.exec",
		TotalRuns:    7,
		TotalHostOps: 42,
		LastDurMs:    350,
		LastStatus:   "ok",
		UpdatedAt:    time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339),
	}
	raw, _ := json.Marshal(stale)
	if err := os.WriteFile(filepath.Join(dataDir, "live-status.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	RunStatusLine(strings.NewReader(""), &out, dataDir)
	if strings.Contains(out.String(), "PTC running") {
		t.Fatalf("stale live-status should not render as running, got: %s", out.String())
	}
	if !strings.Contains(out.String(), "7 runs") {
		t.Fatalf("expected fallback idle summary with run count, got: %s", out.String())
	}

	// Fresh Active=true MUST render as running.
	fresh := stale
	fresh.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	rawFresh, _ := json.Marshal(fresh)
	if err := os.WriteFile(filepath.Join(dataDir, "live-status.json"), rawFresh, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	RunStatusLine(strings.NewReader(""), &out, dataDir)
	if !strings.Contains(out.String(), "PTC running") {
		t.Fatalf("fresh active live-status should render as running, got: %s", out.String())
	}
}
