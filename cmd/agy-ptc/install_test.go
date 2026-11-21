package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteExecutableSafeAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "agy-ptc.exe")

	if err := writeExecutableSafe(dst, []byte("v1-binary-payload")); err != nil {
		t.Fatalf("initial writeExecutableSafe failed: %v", err)
	}
	if err := writeExecutableSafe(dst, []byte("v2-updated-payload")); err != nil {
		t.Fatalf("second writeExecutableSafe failed: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read replaced binary: %v", err)
	}
	if string(got) != "v2-updated-payload" {
		t.Fatalf("got %q, want %q", string(got), "v2-updated-payload")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".agy-ptc-bin-") {
			t.Fatalf("leftover temp file found: %s", e.Name())
		}
	}
}

func TestCopyDirCleanRemovesStaleFilesAndKeepsBinary(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.MkdirAll(filepath.Join(src, "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "plugin.json"), []byte(`{"name":"agy-ptc"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "rules", "AGENTS.md"), []byte("# rules"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pre-populate destination with a stale file, a stale dir, and the installed executable.
	if err := os.MkdirAll(filepath.Join(dst, "obsolete"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "obsolete", "old.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dst, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "scripts", "agy-ptc.exe"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := copyDirClean(src, dst); err != nil {
		t.Fatalf("copyDirClean failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "obsolete", "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected stale file to be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "obsolete")); !os.IsNotExist(err) {
		t.Fatalf("expected stale directory to be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "scripts", "agy-ptc.exe")); err != nil {
		t.Fatalf("expected installed binary to be preserved, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "rules", "AGENTS.md")); err != nil {
		t.Fatalf("expected new rules file to exist, err=%v", err)
	}
}
