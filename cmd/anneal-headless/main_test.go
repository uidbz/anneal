package main

import "testing"

func TestSinkRunFiltering(t *testing.T) {
	s := &sink{}
	// No await yet: everything with a numeric run id passes.
	if !s.acceptRun(map[string]any{"run": float64(1)}) {
		t.Fatal("unexpected drop before any run")
	}
	// Non-run events always pass.
	if !s.acceptRun(map[string]any{"type": "provider.state"}) {
		t.Fatal("provider event filtered")
	}
	if !s.acceptRun(map[string]any{"type": "improve.start", "run": "20260101-000000"}) {
		t.Fatal("string run id (improve) filtered")
	}

	s.setAwait(2)
	if s.acceptRun(map[string]any{"run": float64(1)}) {
		t.Fatal("stale run accepted while awaiting 2")
	}
	if !s.acceptRun(map[string]any{"run": float64(2)}) {
		t.Fatal("awaited run dropped")
	}

	// Abort run 2: its late events (including the finish) are dropped, then
	// the sink accepts events again.
	s.discardRun(2)
	if s.acceptRun(map[string]any{"type": "text.delta", "run": float64(2)}) {
		t.Fatal("aborted run delta accepted")
	}
	if s.acceptRun(map[string]any{"type": "run.finished", "run": float64(2)}) {
		t.Fatal("aborted run finish accepted")
	}
	if !s.acceptRun(map[string]any{"run": float64(3)}) {
		t.Fatal("sink stayed closed after the stale run")
	}
}

func TestSinkDiscardZeroIsNoop(t *testing.T) {
	s := &sink{}
	s.setAwait(5)
	s.discardRun(0)
	if !s.acceptRun(map[string]any{"run": float64(5)}) {
		t.Fatal("discardRun(0) changed the awaited run")
	}
}
