package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/uidbz/anneal/core/events"
)

func TestApplyRunWork(t *testing.T) {
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{bus: events.New()}

	// Empty work dir: no-op, cwd reported as-is.
	r.runWork = ""
	cwd, restore := r.applyRunWork()
	if filepath.Clean(cwd) != filepath.Clean(prev) {
		t.Fatalf("empty runWork: cwd = %q, want %q", cwd, prev)
	}
	restore()
	if wd, _ := os.Getwd(); wd != prev {
		t.Fatalf("empty runWork restore moved cwd to %q", wd)
	}

	// Set work dir: process chdirs there; restore (idempotently) goes back.
	work := t.TempDir()
	r.runWork = work
	cwd, restore = r.applyRunWork()
	if filepath.Clean(cwd) != filepath.Clean(work) {
		t.Fatalf("runWork = %q, applyRunWork reported cwd %q", work, cwd)
	}
	if wd, _ := os.Getwd(); filepath.Clean(wd) != filepath.Clean(work) {
		t.Fatalf("process cwd = %q, want %q", wd, work)
	}
	restore()
	restore() // Once-guarded: the deferred safety net must not double-chdir
	if wd, _ := os.Getwd(); wd != prev {
		t.Fatalf("after restore: process cwd = %q, want %q", wd, prev)
	}

	// Missing directory: no-op with the startup cwd (the server side
	// creates work dirs; applyRunWork never fails a run on chdir).
	r.runWork = filepath.Join(work, "gone")
	cwd, restore = r.applyRunWork()
	if filepath.Clean(cwd) != filepath.Clean(prev) {
		t.Fatalf("missing dir: cwd = %q, want %q", cwd, prev)
	}
	restore()
}
