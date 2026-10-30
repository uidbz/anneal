package main

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	coreapp "github.com/uidbz/anneal/core/app"
	"github.com/uidbz/anneal/core/events"
)

// runHarnessPane executes the embedded harness.risor against the real
// (temp-home) harness API and waits for the main body to finish. -check-ui
// only compiles; this catches runtime errors that would leave the pane empty.
func runHarnessPane(t *testing.T) *scriptPane {
	t.Helper()
	t.Setenv("ANNEAL_HOME", t.TempDir())
	t.Setenv("ANNEAL_PANE_DIR", "")
	t.Setenv("ANNEAL_UI_DIR", "")
	a, err := coreapp.New()
	if err != nil {
		t.Fatal(err)
	}
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
	return p
}

func TestHarnessPaneExecutes(t *testing.T) {
	p := runHarnessPane(t)
	if n := len(p.view.Window().GetContentContainer().Objects); n == 0 {
		t.Fatal("pane rendered no content")
	}
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
