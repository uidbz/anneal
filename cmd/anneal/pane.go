package main

import (
	"fmt"
	"os"
	"strings"

	"fyne.io/fyne/v2"
	"github.com/uidbz/anneal/core/events"
	"github.com/uidbz/anneal/core/uiapi"
	"github.com/uidbz/fynerisor/gui"
)

// paneCapabilities declares what a scripted pane may do. The default for
// agent/user-authored panels should be pull-only (no host window, no live
// events); the built-in harness pane opts into the filtered event feed.
type paneCapabilities struct {
	Events     bool // expose a filtered agent.on feed
	HostWindow bool // expose dialogs, shortcuts, drops, window.Close
}

// harnessPaneCaps: pull APIs + lifecycle notices, no host powers.
var harnessPaneCaps = paneCapabilities{Events: true, HostWindow: false}

// scriptPane owns one embedded fynerisor runtime (a gui.CanvasObject) and its
// optional script bridge. Keeping the capability decision in one place makes
// it hard to accidentally grant a pane host-level powers.
type scriptPane struct {
	view   *gui.CanvasObject
	bridge *uiapi.Bridge
}

// newScriptPane builds and compiles a pane. api is the pane-facing global
// (usually a narrow map from App); when Events is enabled, a filtered agent.on
// builtin is added to it. bus/filter may be nil for compile-only checks.
func newScriptPane(host fyne.Window, src string, caps paneCapabilities,
	bus *events.Bus, api map[string]any, filter func(any) bool) (*scriptPane, error) {
	opts := []gui.Option{
		gui.WithJSON(),
		gui.WithStrings(),
		gui.WithStatusCallback(func(status string) {
			if strings.HasPrefix(status, "ERROR") {
				fmt.Fprintln(os.Stderr, "anneal: pane:", status)
			}
		}),
	}
	if caps.HostWindow && host != nil {
		opts = append(opts, gui.WithHostWindow(host))
	}
	view := gui.NewCanvasObject(src, opts...)
	p := &scriptPane{view: view}
	if caps.Events {
		p.bridge = uiapi.NewFiltered(bus, filter)
		p.bridge.Attach(view.Window())
		api["on"] = p.bridge.API()["on"]
	}
	view.Window().RegisterGlobal("agent", api)
	if err := view.CompileScript(); err != nil {
		return nil, err
	}
	return p, nil
}

// Execute starts the script; safe to call again after LoadScript.
func (p *scriptPane) Execute() {
	p.view.Execute()
	if p.bridge != nil {
		p.bridge.Ready()
	}
}

// LoadScript replaces the script and compiles it without executing.
func (p *scriptPane) LoadScript(src string) error {
	p.view.LoadScript(src)
	return p.view.CompileScript()
}
