package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyWorkspaceConfinementAndGitGuard(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()

	eng := NewEngine(ws)
	eng.LoadSettingsJSON([]byte(`{}`))

	// Inside workspace write is allowed
	inFile, err := eng.ResolveAndCheckWrite(ws, "internal/app.go")
	if err != nil {
		t.Fatalf("expected workspace write allowed, got: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(inFile), "internal/app.go") {
		t.Fatalf("unexpected resolved path: %s", inFile)
	}

	// Outside workspace write is blocked
	_, err = eng.ResolveAndCheckWrite(ws, filepath.Join(outside, "evil.txt"))
	if err == nil {
		t.Fatal("expected outside-workspace write to be blocked")
	}

	// .git/hooks write inside workspace is blocked unconditionally
	_ = os.MkdirAll(filepath.Join(ws, ".git", "hooks"), 0o755)
	_, err = eng.ResolveAndCheckWrite(ws, ".git/hooks/pre-commit")
	if err == nil {
		t.Fatal("expected .git/hooks write to be denied")
	}
}

func TestPolicySettingsPrecedenceAndDestructiveCommands(t *testing.T) {
	ws := t.TempDir()
	eng := NewEngine(ws)
	eng.LoadSettingsJSON([]byte(`{}`))
	eng.LoadSettingsJSON([]byte(`{
		"permissions": {
			"allow": ["command(git)", "mcp(zvec_grep/*)"],
			"ask": ["command(git push)"],
			"deny": ["command(rm -rf)", "write_file(secrets/)", "mcp(zvec_grep/drop_index)"]
		}
	}`))

	// Allow rule
	if err := eng.CheckCommand("git status --short"); err != nil {
		t.Fatalf("expected git status allowed, got: %v", err)
	}
	// Ask overrides Allow
	err := eng.CheckCommand("git push origin main")
	if !IsApprovalRequired(err) {
		t.Fatalf("expected Ask rule to require approval on git push, got: %v", err)
	}
	// Destructive built-in guard
	if err := eng.CheckCommand("git reset --hard HEAD~1"); !IsApprovalRequired(err) {
		t.Fatalf("expected git reset --hard to require host approval, got: %v", err)
	}
	// Deny rule on path
	if _, err := eng.ResolveAndCheckWrite(ws, "secrets/api.key"); err == nil {
		t.Fatal("expected write to secrets/ to be denied")
	}
	// Deny rule overrides wildcard Allow on MCP
	if err := eng.CheckMCP("zvec_grep", "drop_index"); err == nil {
		t.Fatal("expected mcp(zvec_grep/drop_index) to be denied")
	}
	if err := eng.CheckMCP("zvec_grep", "zvec_grep_search"); err != nil {
		t.Fatalf("expected zvec_grep_search allowed, got: %v", err)
	}
}
