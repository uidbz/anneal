// Package uiapi exposes the anneal core to Risor UI scripts.
//
// The bridge is deliberately narrow and JSON-shaped: script calls into core
// are fire-and-forget (never block the Risor VM or the GUI thread), and every
// core→script event is delivered as a JSON string through a single callback
// registered with agent.on(). Handlers run on the GUI thread via
// window.Do + vmguard.Call, so widget mutations inside them are safe.
package uiapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/deepnoodle-ai/risor/v2/pkg/object"
	"github.com/uidbz/anneal/core/events"
	"github.com/uidbz/fynerisor/gui"
	"github.com/uidbz/fynerisor/gui/guithread"
	"github.com/uidbz/fynerisor/gui/vmguard"
)

// Bridge is the object exposed to Risor scripts as the global `agent`. It is
// a script adapter over the UI-agnostic events.Bus.
type Bridge struct {
	bus *events.Bus
	fw  *gui.Window

	mu       sync.Mutex
	handler  *object.Closure
	callFunc object.CallFunc
	queue    []string // events queued before handler registration / Ready
	ready    bool     // true once fw.Execute has created the callback channel
}

// New creates a script bridge over bus; nil is allowed for isolated tests.
// Call Attach once the fynerisor window exists and Ready after Execute (its
// callback channel only exists from then on).
func New(bus *events.Bus) *Bridge {
	b := &Bridge{bus: bus}
	if bus != nil {
		bus.AddTap(b.consume)
	}
	return b
}

// Attach connects the bridge to its fynerisor window (for GUI-thread dispatch).
func (b *Bridge) Attach(fw *gui.Window) {
	b.mu.Lock()
	b.fw = fw
	b.mu.Unlock()
}

// Quit asks the attached Fyne window/app to exit on the GUI thread. It is a
// no-op when the bridge has no window (e.g. the Go shell or tests).
func (b *Bridge) Quit() {
	b.mu.Lock()
	fw := b.fw
	b.mu.Unlock()
	if fw == nil {
		return
	}
	guithread.Do(func() {
		switch {
		case fw.FyneApp != nil:
			fw.FyneApp.Quit()
		case fw.FyneWindow != nil:
			fw.FyneWindow.Close()
		}
	})
}

// On registers the script-side event handler: agent.on((evtJson) => { ... }).
// The callFunc captured from ctx is the VM's own entry point for invoking the
// closure from Go goroutines (guarded by vmguard).
func (b *Bridge) setOn(cb *object.Closure, callFunc object.CallFunc) {
	b.mu.Lock()
	b.handler, b.callFunc = cb, callFunc
	ready, queued := b.ready, b.queue
	if ready {
		b.queue = nil
	}
	b.mu.Unlock()
	if ready {
		for _, ev := range queued {
			b.dispatch(ev)
		}
	}
}

// Ready opens event delivery; call right after fw.Execute, which creates the
// fynerisor callback channel that window.Do sends on. The queue is only
// flushed when a handler is already registered — otherwise it is kept for
// setOn, which is the common case (Execute starts the script asynchronously).
func (b *Bridge) Ready() {
	b.mu.Lock()
	b.ready = true
	queued, haveHandler := b.queue, b.handler != nil
	if haveHandler {
		b.queue = nil
	}
	b.mu.Unlock()
	if haveHandler {
		for _, ev := range queued {
			b.dispatch(ev)
		}
	}
}

// Emit publishes an event. With a bus, the bus fans it out to every observer
// (including this bridge); without one it is delivered directly (tests).
func (b *Bridge) Emit(v any) {
	if b.bus != nil {
		b.bus.Emit(v)
		return
	}
	b.consume(v)
}

// consume is the bus observer for script delivery: marshal to JSON, queue
// before the handler/Ready, then dispatch on the GUI thread.
func (b *Bridge) consume(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "uiapi: marshal event: %v\n", err)
		return
	}
	payload := string(data)
	b.mu.Lock()
	if !b.ready || b.handler == nil {
		if len(b.queue) < 4096 {
			b.queue = append(b.queue, payload)
		}
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	b.dispatch(payload)
}

// dispatch runs the handler on the GUI thread; a panicking or erroring script
// handler is logged, never fatal to the process. Contention with a still
// running script (vmguard) reschedules the delivery instead of dropping it.
func (b *Bridge) dispatch(jsonStr string) {
	b.deliver(jsonStr, 0)
}

func (b *Bridge) deliver(jsonStr string, attempt int) {
	b.mu.Lock()
	fw, ready := b.fw, b.ready
	b.mu.Unlock()
	if fw == nil || !ready {
		return
	}
	fw.Do(func() {
		b.mu.Lock()
		cb, callFunc := b.handler, b.callFunc
		b.mu.Unlock()
		if cb == nil || callFunc == nil {
			return
		}
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "uiapi: recovered panic in script handler: %v\n%s\n", r, debug.Stack())
			}
		}()
		_, err := vmguard.Call(callFunc, context.Background(), cb, []object.Object{object.NewString(jsonStr)})
		if err == nil {
			return
		}
		if errors.Is(err, vmguard.ErrConcurrentAccess) && attempt < 40 {
			time.AfterFunc(time.Duration(25*(attempt+1))*time.Millisecond, func() { b.deliver(jsonStr, attempt+1) })
			return
		}
		fmt.Fprintf(os.Stderr, "uiapi: script handler error: %v\n", err)
	})
}

// API returns the object exposed to scripts as the `agent` global. Values
// that are already risor objects (Builtin) pass through the global conversion
// untouched. Keep this list in sync with docs/PLAN.md §7 (bridge contract).
func (b *Bridge) API() map[string]any {
	return map[string]any{
		"on": object.NewBuiltin("agent.on", func(ctx context.Context, args ...object.Object) (object.Object, error) {
			if len(args) != 1 {
				return object.Errorf("agent.on: expects 1 argument, got %d", len(args)), nil
			}
			cb, ok := args[0].(*object.Closure)
			if !ok {
				return object.Errorf("agent.on: expected a function"), nil
			}
			callFunc, ok := object.GetCallFunc(ctx)
			if !ok {
				return object.Errorf("agent.on: unable to get call function"), nil
			}
			b.setOn(cb, callFunc)
			return object.Nil, nil
		}),
		"quit": object.NewBuiltin("agent.quit", func(context.Context, ...object.Object) (object.Object, error) {
			b.Quit()
			return object.Nil, nil
		}),
	}
}
