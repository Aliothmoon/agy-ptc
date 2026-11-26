package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUninstallPurgeRemovesAllDataDirsAndReportsOnlyExisting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GOBIN", t.TempDir()) // keep --purge away from the real CLI binary
	t.Setenv("PATH", "")           // do not call a real `agy plugin disable`

	present := []string{
		filepath.Join(home, ".gemini", "config", "plugin_data", pluginName),
		filepath.Join(home, ".gemini", "cache", pluginName),
	}
	for _, d := range present {
		if err := os.MkdirAll(filepath.Join(d, "logs"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	absent := filepath.Join(home, ".gemini", "antigravity-cli", "plugin_data", pluginName)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	runErr := runUninstall([]string{"--purge"})
	os.Stdout = stdout
	_ = w.Close()
	out, _ := io.ReadAll(r)

	if runErr != nil {
		t.Fatalf("runUninstall: %v", runErr)
	}
	for _, d := range present {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, stat err=%v", d, err)
		}
		if !strings.Contains(string(out), "Removed plugin data "+d) {
			t.Errorf("missing removal message for %s:\n%s", d, out)
		}
	}
	if strings.Contains(string(out), absent) {
		t.Errorf("reported removing a directory that never existed:\n%s", out)
	}
}
