package uiapi

import (
	"sync"
	"testing"
	"time"
)

// Regression: Ready used to clear the pre-handler queue when agent.on had not
// been registered yet (Execute runs the script asynchronously), losing the
// initial session replay events.
func TestReadyKeepsQueueUntilHandler(t *testing.T) {
	b := New()
	b.Emit(map[string]any{"type": "session.opened"})
	b.Emit(map[string]any{"type": "mode.changed"})
	b.Ready()

	b.mu.Lock()
	queued := len(b.queue)
	b.mu.Unlock()
	if queued != 2 {
		t.Fatalf("Ready dropped queued events: %d", queued)
	}

	// After Ready without a handler, new events keep queueing (never dropped).
	b.Emit(map[string]any{"type": "provider.state"})
	b.mu.Lock()
	queued = len(b.queue)
	b.mu.Unlock()
	if queued != 3 {
		t.Fatalf("event emitted before handler was not queued: %d", queued)
	}
}

// The tap runs user code; it must not be invoked while holding the bridge
// mutex (a tap that calls back into the bridge would deadlock).
func TestTapMayReenterBridge(t *testing.T) {
	b := New()
	var once sync.Once
	b.SetTap(func(any) {
		once.Do(func() { b.SetTap(nil) })
	})
	done := make(chan struct{})
	go func() {
		b.Emit(map[string]any{"type": "x"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Emit deadlocked while calling the tap")
	}
}

// AddTap must compose observers (journey log + Go UI) instead of replacing.
func TestAddTapComposes(t *testing.T) {
	b := New()
	var first, second int
	b.SetTap(func(any) { first++ })
	b.AddTap(func(any) { second++ })
	b.Emit(map[string]any{"type": "x"})
	if first != 1 || second != 1 {
		t.Fatalf("taps: first=%d second=%d", first, second)
	}
}

// A tap-only bridge is consumed by Go code: nothing is queued or marshaled.
func TestTapOnlySkipsQueue(t *testing.T) {
	b := New()
	b.SetTapOnly()
	var got int
	b.AddTap(func(any) { got++ })
	b.Emit(map[string]any{"type": "x"})
	if got != 1 {
		t.Fatalf("tap calls: %d", got)
	}
	b.mu.Lock()
	queued := len(b.queue)
	b.mu.Unlock()
	if queued != 0 {
		t.Fatalf("tap-only bridge queued %d events", queued)
	}
}

func TestQuitWithoutWindowIsNoop(t *testing.T) {
	New().Quit() // must not panic before Attach
}
