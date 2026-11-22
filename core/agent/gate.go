package agent

import (
	"context"
)

// RunGate serializes runs that share process-wide state. The Runner already
// serializes itself, but anneal can have more than one Runner in a process
// (for example the foreground runner and a scheduled-job worker). Both call
// os.Chdir for per-run work directories, so only one may run at a time.
type RunGate struct {
	ch chan struct{}
}

// NewRunGate returns a free gate.
func NewRunGate() *RunGate {
	g := &RunGate{ch: make(chan struct{}, 1)}
	g.ch <- struct{}{}
	return g
}

// TryAcquire takes the gate without blocking.
func (g *RunGate) TryAcquire() bool {
	if g == nil {
		return true
	}
	select {
	case <-g.ch:
		return true
	default:
		return false
	}
}

// Acquire waits for the gate or ctx cancellation.
func (g *RunGate) Acquire(ctx context.Context) error {
	if g == nil {
		return nil
	}
	select {
	case <-g.ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release returns the gate. It is safe to call more times than acquired; only
// one token exists.
func (g *RunGate) Release() {
	if g == nil {
		return
	}
	select {
	case g.ch <- struct{}{}:
	default:
	}
}
