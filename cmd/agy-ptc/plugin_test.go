package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
