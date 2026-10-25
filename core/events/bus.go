// Package events is the UI-agnostic event bus used by the agent, review loop,
// and improvement pipeline. Consumers attach taps: the Go shell, the journey
// log, the headless sink, or a script adapter. The bus has no Fyne/Risor
// dependency, which keeps the headless binary free of GUI libraries.
package events

import "sync"

// Bus fans out events to registered observers in registration order.
// Emit is safe from any goroutine; observers must not block (delivery is
// synchronous) and should hand off to their own queue when needed.
type Bus struct {
	mu   sync.Mutex
	taps []func(any)
}

func New() *Bus { return &Bus{} }

// SetTap replaces all observers with fn (nil clears).
func (b *Bus) SetTap(fn func(any)) {
	b.mu.Lock()
	if fn == nil {
		b.taps = nil
	} else {
		b.taps = []func(any){fn}
	}
	b.mu.Unlock()
}

// AddTap appends an observer without replacing existing ones.
func (b *Bus) AddTap(fn func(any)) {
	if fn == nil {
		return
	}
	b.mu.Lock()
	b.taps = append(b.taps, fn)
	b.mu.Unlock()
}

// Emit publishes v to a snapshot of the observers. It never holds the lock
// while calling user code.
func (b *Bus) Emit(v any) {
	if b == nil {
		return
	}
	b.mu.Lock()
	taps := append([]func(any){}, b.taps...)
	b.mu.Unlock()
	for _, tap := range taps {
		tap(v)
	}
}
