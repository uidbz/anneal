package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadImprovementRuns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ANNEAL_HOME", home)
	dir := filepath.Join(home, "improvements", "run-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	step := `{"run_id":"run-1","task":"fix it","k":1,"mode":"sequential",
		"candidates":[{"kind":"invariant","probe":{"steps":[{"name":"Read"}]}}],
		"decisions":"adopt","successor":"/tmp/compose","library_entry":"lib-1"}`
	if err := os.WriteFile(filepath.Join(dir, "step.json"), []byte(step), 0o644); err != nil {
		t.Fatal(err)
	}
	// A corrupt record must be skipped, not fatal.
	badDir := filepath.Join(home, "improvements", "bad")
	os.MkdirAll(badDir, 0o755)
	os.WriteFile(filepath.Join(badDir, "step.json"), []byte("{"), 0o644)

	runs := loadImprovementRuns(10)
	if len(runs) != 1 {
		t.Fatalf("runs: %+v", runs)
	}
	r := runs[0]
	if r.RunID != "run-1" || len(r.Candidates) != 1 || r.Candidates[0].Probe == nil ||
		len(r.Candidates[0].Probe.Steps) != 1 || r.LibraryEntry != "lib-1" {
		t.Fatalf("decoded run: %+v", r)
	}
	if got := loadImprovementRuns(1); len(got) != 1 {
		t.Fatalf("limit: %d", len(got))
	}
}
