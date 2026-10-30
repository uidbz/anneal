package main

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	coreapp "github.com/uidbz/anneal/core/app"
	"github.com/uidbz/anneal/core/events"
)

// runHarnessPane executes the embedded harness.risor against the real
// (temp-home) harness API and waits for the main body to finish. -check-ui
// only compiles; this catches runtime errors that would leave the pane empty.
func runHarnessPane(t *testing.T) (*scriptPane, *coreapp.App) {
	t.Helper()
	t.Setenv("ANNEAL_HOME", t.TempDir())
	t.Setenv("ANNEAL_PANE_DIR", "")
	t.Setenv("ANNEAL_UI_DIR", "")
	a, err := coreapp.New()
	if err != nil {
		t.Fatal(err)
	}
	seedHarnessFiles(t, a)
	fyneApp := test.NewApp()
	t.Cleanup(fyneApp.Quit)
	win := fyneApp.NewWindow("host")
	src, err := paneSource("")
	if err != nil {
		t.Fatal(err)
	}
	p, err := newScriptPane(win, src, harnessPaneCaps, a.Bus, a.HarnessAPI(), paneEvent)
	if err != nil {
		t.Fatal(err)
	}
	win.SetContent(p.view)
	p.Execute()
	t.Cleanup(p.view.Stop)
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := p.view.Window().Status()
		if st == "Ready!" {
			break
		}
		if strings.HasPrefix(st, "ERROR") {
			t.Fatalf("pane script failed: %s", st)
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane did not finish; status %q", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return p, a
}

func TestHarnessPaneExecutes(t *testing.T) {
	p, _ := runHarnessPane(t)
	if n := len(p.view.Window().GetContentContainer().Objects); n == 0 {
		t.Fatal("pane rendered no content")
	}
}

// walkPane visits every object reachable from the pane's content, including
// children of custom widget renderers.
func walkPane(o fyne.CanvasObject, visit func(fyne.CanvasObject)) {
	seen := map[fyne.CanvasObject]bool{}
	var walk func(fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		if o == nil || seen[o] {
			return
		}
		seen[o] = true
		visit(o)
		switch v := o.(type) {
		case *fyne.Container:
			for _, c := range v.Objects {
				walk(c)
			}
		case fyne.Widget:
			for _, c := range test.WidgetRenderer(v).Objects() {
				walk(c)
			}
		}
	}
	walk(o)
}

func paneEntry(root fyne.CanvasObject) *widget.Entry {
	var found *widget.Entry
	walkPane(root, func(o fyne.CanvasObject) {
		if e, ok := o.(*widget.Entry); ok && e.MultiLine && found == nil {
			found = e
		}
	})
	return found
}

func paneTexts(root fyne.CanvasObject) string {
	var b strings.Builder
	walkPane(root, func(o fyne.CanvasObject) {
		if l, ok := o.(*widget.Label); ok {
			b.WriteString(l.Text)
			b.WriteString("\n")
		}
	})
	return b.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHarnessPaneUnsavedGuard(t *testing.T) {
	p, a := runHarnessPane(t)
	root := p.view.Window().GetContentContainer()
	entry := paneEntry(root)
	if entry == nil {
		t.Fatal("pane has no harness editor")
	}
	original := entry.Text
	entry.SetText(original + "\n# local edit")
	waitFor(t, "unsaved notice", func() bool {
		return strings.Contains(paneTexts(root), "unsaved changes")
	})

	// First refresh keeps the edits and asks for confirmation.
	a.Bus.Emit(map[string]any{"type": "harness.refresh"})
	time.Sleep(100 * time.Millisecond)
	if got := paneEntry(root); got == nil || got.Text == original {
		t.Fatal("pane refresh discarded unsaved edits without confirmation")
	}

	// Second refresh discards them and rebuilds from disk.
	a.Bus.Emit(map[string]any{"type": "harness.refresh"})
	waitFor(t, "rebuild after forced refresh", func() bool {
		e := paneEntry(root)
		return e != nil && e.Text == original
	})
}

func TestScriptPaneCapabilities(t *testing.T) {
	fyneApp := test.NewApp()
	defer fyneApp.Quit()
	win := fyneApp.NewWindow("host")

	// Default agent-authored pane: no host window, no event feed.
	api := map[string]any{}
	sp, err := newScriptPane(win, `window.SetContent(widget.NewLabel("x"))`,
		paneCapabilities{}, events.New(), api, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sp.view.Window().FyneWindow != nil {
		t.Fatal("host window granted without the capability")
	}
	if _, ok := api["on"]; ok {
		t.Fatal("event feed exposed without the capability")
	}

	// Explicit opt-in exposes the host and the filtered feed.
	api2 := map[string]any{}
	sp2, err := newScriptPane(win, `window.SetContent(widget.NewLabel("y"))`,
		paneCapabilities{Events: true, HostWindow: true}, events.New(), api2,
		func(any) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if sp2.view.Window().FyneWindow != win {
		t.Fatal("host window not attached")
	}
	if _, ok := api2["on"]; !ok {
		t.Fatal("event feed missing despite the capability")
	}
}

// seedHarnessFiles gives the pane real components to edit (the temp home only
// seeds README.md).
func seedHarnessFiles(t *testing.T, a *coreapp.App) {
	t.Helper()
	if err := a.Harness.SaveFile("system.md", "# Harness\n\nYou are Anneal, a coding agent.\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.Harness.SaveFile("guardrails.md", "# Guardrails\n\n- Never force-push.\n"); err != nil {
		t.Fatal(err)
	}
}
