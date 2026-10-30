package app

import (
	"strings"
	"testing"
	"time"
)

// newAutoTestApp builds an App in a temp home with the reviewer off. The
// provider is unreachable (default localhost), so an improve pass fails
// fast — which is fine: these tests verify the arm/fire/guard wiring,
// not the improvement engine (covered in core/improve).
func newAutoTestApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("ANNEAL_HOME", t.TempDir())
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg.Review.Enabled = false
	a.Cfg.Improve.Enabled = true
	return a
}

func watchTypes(a *App, types ...string) chan map[string]any {
	want := map[string]bool{}
	for _, ty := range types {
		want[ty] = true
	}
	ch := make(chan map[string]any, 32)
	a.Bus.AddTap(func(v any) {
		m, ok := v.(map[string]any)
		if !ok || !want[toStr(m["type"])] {
			return
		}
		select {
		case ch <- m:
		default:
		}
	})
	return ch
}

func TestAutoImproveArmsAndFires(t *testing.T) {
	a := newAutoTestApp(t)
	a.Cfg.Improve.Auto = true
	a.Cfg.Improve.AutoIdleS = 1
	got := watchTypes(a, "improve.auto", "improve.error")

	a.Bus.Emit(map[string]any{"type": "run.finished", "ok": true})

	var auto, errEv map[string]any
	deadline := time.After(8 * time.Second)
	for auto == nil || errEv == nil {
		select {
		case m := <-got:
			if m["type"] == "improve.auto" {
				auto = m
			} else {
				errEv = m
			}
		case <-deadline:
			t.Fatalf("improve.auto=%v improve.error=%v", auto, errEv)
		}
	}
	if e, _ := errEv["error"].(string); !strings.Contains(e, "auto:") {
		t.Errorf("auto pass error should be tagged: %v", errEv)
	}
}

func TestAutoImproveOffAndOncePerSession(t *testing.T) {
	// Disabled: nothing arms.
	a := newAutoTestApp(t)
	a.Cfg.Improve.Auto = false
	a.Cfg.Improve.AutoIdleS = 1
	got := watchTypes(a, "improve.auto", "improve.error")
	a.Bus.Emit(map[string]any{"type": "run.finished", "ok": true})
	select {
	case m := <-got:
		t.Fatalf("auto=false must not fire: %v", m)
	case <-time.After(1500 * time.Millisecond):
	}

	// Enabled: the first pass marks the session; a later run must not arm
	// a second pass.
	b := newAutoTestApp(t)
	b.Cfg.Improve.Auto = true
	b.Cfg.Improve.AutoIdleS = 1
	gotB := watchTypes(b, "improve.auto", "improve.error")
	b.Bus.Emit(map[string]any{"type": "run.finished", "ok": true})
	select {
	case m := <-gotB:
		if m["type"] != "improve.auto" && m["type"] != "improve.error" {
			t.Fatalf("unexpected first event: %v", m)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("first pass never fired")
	}
	// Drain the pass error, then re-run: nothing new may arrive.
drain:
	for {
		select {
		case m := <-gotB:
			if m["type"] == "improve.auto" {
				t.Fatalf("second arm before quiescence: %v", m)
			}
			continue // the auto error from pass 1
		case <-time.After(2500 * time.Millisecond):
			break drain // quiescent
		}
	}
	b.Bus.Emit(map[string]any{"type": "run.finished", "ok": true})
	select {
	case m := <-gotB:
		t.Fatalf("session already improved; re-arm must be skipped: %v", m)
	case <-time.After(2 * time.Second):
	}
}
