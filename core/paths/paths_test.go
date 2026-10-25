package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootEnv(t *testing.T) {
	t.Setenv("ANNEAL_HOME", "/tmp/anneal-new")
	if got := Root(); got != "/tmp/anneal-new" {
		t.Fatalf("ANNEAL_HOME: %q", got)
	}
}

func TestRootMigratesLegacyDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANNEAL_HOME", "")
	oldDir := filepath.Join(home, ".fyagent")
	if err := os.MkdirAll(filepath.Join(oldDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Root()
	newDir := filepath.Join(home, ".anneal")
	if got != newDir {
		t.Fatalf("Root = %q, want %q", got, newDir)
	}
	if b, err := os.ReadFile(filepath.Join(newDir, "config.json")); err != nil || string(b) != "{}" {
		t.Fatalf("config not migrated: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(oldDir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("legacy dir still populated after migration")
	}
}

func TestRootPrefersExistingNewDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANNEAL_HOME", "")
	if err := os.MkdirAll(filepath.Join(home, ".anneal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".fyagent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Root(); got != filepath.Join(home, ".anneal") {
		t.Fatalf("existing .anneal should be kept: %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".fyagent")); err != nil {
		t.Fatal("legacy dir must be left alone when .anneal exists")
	}
}
