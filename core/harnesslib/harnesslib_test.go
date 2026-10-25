package harnesslib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/anneal/core/paths"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s := New(filepath.Join(t.TempDir(), "harness-lib"))
	if err := s.Seed(); err != nil {
		t.Fatal(err)
	}
	return s
}

func succDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "compose")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "system.md"), []byte("always verify"), 0o644)
	os.WriteFile(filepath.Join(d, "decisions.md"), []byte("adopt"), 0o644) // excluded
	return d
}

func TestCreateListContext(t *testing.T) {
	s := newStore(t)
	id, err := s.CreateEntry(succDir(t), "Add Sub function with tests", "sess1", "run1", "abc",
		"## situation\ntable-test tasks; missing verification\n## lessons\n- verify rule → re-read files\n## evidence\n- quoted output")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "20") || !strings.Contains(id, "add-sub-function") {
		t.Fatalf("id: %s", id)
	}
	// decisions.md must not become a component
	info := s.List()
	if len(info) != 1 || strings.Contains(info[0].Components, "decisions") {
		t.Fatalf("list: %+v", info)
	}
	if info[0].Situation == "" {
		t.Fatal("situation not parsed")
	}
	// entry rejected when experience empty
	if _, err := s.CreateEntry(succDir(t), "x", "", "", "", "  "); err == nil {
		t.Fatal("empty experience accepted")
	}
	// same task in the same second must not collide
	a, err := s.CreateEntry(succDir(t), "collide task", "s", "r", "h", "lesson lesson lesson lesson lesson lesson")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateEntry(succDir(t), "collide task", "s", "r", "h", "other lesson other lesson other lesson ok")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("id collision: %s", a)
	}
	// experience read + id jail
	x, err := s.Experience(b)
	if err != nil || !strings.Contains(x, "other lesson") {
		t.Fatalf("experience: %q %v", x, err)
	}
	if _, err := s.Experience("../x"); err == nil {
		t.Fatal("bad id accepted")
	}
	ctx := s.Context("verify files before done", 2, 2000)
	if !strings.Contains(ctx, "verify rule") {
		t.Fatalf("context: %q", ctx)
	}
	if s.Context("zzz qqq", 2, 0) == "" {
		t.Fatal("context should still return something when non-empty library")
	}
}

func TestPromoteWithoutReplacementHasNoBackup(t *testing.T) {
	t.Setenv("ANNEAL_HOME", t.TempDir())
	s := newStore(t)
	d := filepath.Join(t.TempDir(), "compose")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "tool-guidance.md"), []byte("guide"), 0o644)
	id, err := s.CreateEntry(d, "task", "", "", "", "## situation\ns\n## lessons\n- l\n## evidence\n- e")
	if err != nil {
		t.Fatal(err)
	}
	backup, err := s.Promote(id)
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Fatalf("backup path returned although nothing was replaced: %q", backup)
	}
	if _, err := os.Stat(filepath.Join(paths.Harness(), "tool-guidance.md")); err != nil {
		t.Fatalf("component not promoted: %v", err)
	}
}

func TestPromoteBacksUp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ANNEAL_HOME", home)
	s := newStore(t)
	os.MkdirAll(paths.Harness(), 0o755)
	os.WriteFile(filepath.Join(paths.Harness(), "system.md"), []byte("old global"), 0o644)

	id, err := s.CreateEntry(succDir(t), "task", "", "", "", "## situation\ns\n## lessons\n- l\n## evidence\n- e")
	if err != nil {
		t.Fatal(err)
	}
	backup, err := s.Promote(id)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(paths.Harness(), "system.md"))
	if string(got) != "always verify" {
		t.Fatalf("global not promoted: %q", got)
	}
	old, err := os.ReadFile(filepath.Join(backup, "system.md"))
	if err != nil || string(old) != "old global" {
		t.Fatalf("backup: %q %v", old, err)
	}
	if _, err := s.Promote("nope"); err == nil {
		t.Fatal("unknown entry promoted")
	}
	if _, err := s.Promote("a/b"); err == nil {
		t.Fatal("path-y id accepted")
	}
}
