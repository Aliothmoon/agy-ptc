package version

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPluginManifestMatchesVersion(t *testing.T) {
	raw, err := os.ReadFile("../../plugin/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != Version {
		t.Fatalf("plugin/plugin.json version %q != internal/version %q", manifest.Version, Version)
	}
}
