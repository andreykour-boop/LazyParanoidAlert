package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveConfigPath: an explicit -config passes through untouched; the
// default name resolves in the working directory when present; with no
// config anywhere the default stays as is (the error then names the real
// problem instead of a silent wrong path).
func TestResolveConfigPath(t *testing.T) {
	if got := resolveConfigPath("/opt/siren/my.json"); got != "/opt/siren/my.json" {
		t.Errorf("explicit path must pass through, got %q", got)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if got := resolveConfigPath("config.json"); got != "config.json" {
		t.Errorf("cwd config must be kept, got %q", got)
	}

	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got := resolveConfigPath("config.json"); got != "config.json" {
		t.Errorf("missing config must stay default, got %q", got)
	}
}
