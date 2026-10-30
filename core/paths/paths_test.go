package paths

import (
	"path/filepath"
	"testing"
)

func TestRootEnv(t *testing.T) {
	t.Setenv("ANNEAL_HOME", "/tmp/anneal-new")
	if got := Root(); got != "/tmp/anneal-new" {
		t.Fatalf("ANNEAL_HOME: %q", got)
	}
}

func TestRootDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANNEAL_HOME", "")
	if got := Root(); got != filepath.Join(home, ".anneal") {
		t.Fatalf("Root = %q, want ~/.anneal", got)
	}
}
