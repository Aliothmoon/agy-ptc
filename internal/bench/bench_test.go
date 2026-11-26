package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeFileReportsOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	data := `{"type":"PLANNER_RESPONSE"}` + "\n" + strings.Repeat("x", 17*1024*1024) + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AnalyzeFile(path); err == nil {
		t.Fatal("expected an error instead of a silently truncated report")
	}
}
