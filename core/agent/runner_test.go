package agent

import (
	"context"
	"testing"
)

func TestClearRunIgnoresStaleRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runner{runSeq: 2, running: true, cancel: cancel}
	r.clearRun(1) // a late defer from the previous run
	if !r.running {
		t.Fatal("stale clearRun marked the new run idle")
	}
	if ctx.Err() != nil {
		t.Fatal("stale clearRun cancelled the new run")
	}
	r.clearRun(2)
	if r.running || r.cancel != nil {
		t.Fatal("current run not cleared")
	}
	if ctx.Err() == nil {
		t.Fatal("current run context not cancelled")
	}
}
