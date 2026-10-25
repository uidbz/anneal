package events

import (
	"sync"
	"testing"
	"time"
)

func TestTapCompositionAndOrder(t *testing.T) {
	b := New()
	var mu sync.Mutex
	var order []string
	append1 := func(name string) func(any) {
		return func(any) {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
		}
	}
	b.SetTap(append1("first"))
	b.AddTap(append1("second"))
	b.Emit(map[string]any{"type": "x"})

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("order: %v", order)
	}
}

// A tap may re-enter the bus (SetTap/AddTap/Emit) without deadlocking.
func TestTapMayReenterBus(t *testing.T) {
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
		t.Fatal("Emit deadlocked while calling a tap")
	}
}

func TestNilBusAndTap(t *testing.T) {
	var b *Bus
	b.Emit(map[string]any{"type": "x"}) // no panic
	New().AddTap(nil)
	New().SetTap(nil)
}
