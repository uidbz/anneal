package main

import (
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/uidbz/anneal/core/events"
)

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
