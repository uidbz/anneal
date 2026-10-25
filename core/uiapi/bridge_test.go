package uiapi

import "testing"

func newBridge() *Bridge { return New(nil) }

// Regression: Ready used to clear the pre-handler queue when agent.on had not
// been registered yet (Execute runs the script asynchronously), losing the
// initial session replay events.
func TestReadyKeepsQueueUntilHandler(t *testing.T) {
	b := newBridge()
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

func TestQuitWithoutWindowIsNoop(t *testing.T) {
	newBridge().Quit() // must not panic before Attach
}
