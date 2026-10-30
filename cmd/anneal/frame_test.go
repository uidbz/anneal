package main

import (
	"testing"

	"fyne.io/fyne/v2"
)

func TestShowViewSwitchesAndTracksRail(t *testing.T) {
	sh := newTestShell(t)
	for _, v := range []viewID{viewHarness, viewActivity, viewSettings, viewChat} {
		sh.showView(v)
		if sh.view != v {
			t.Fatalf("view = %v, want %v", sh.view, v)
		}
		for id, view := range sh.views {
			if got := view.Visible(); got != (id == v) {
				t.Fatalf("view %v visible=%v while showing %v", id, got, v)
			}
		}
		for id, item := range sh.navRail {
			if item.active != (id == v) {
				t.Fatalf("rail item %v active=%v while showing %v", id, item.active, v)
			}
		}
	}
}

func TestPanelTogglesPersist(t *testing.T) {
	sh := newTestShell(t)
	sh.a.Cfg.UI.SidebarOpen = true
	sh.frame.SetSidebar(true)
	sh.toggleSidebar()
	if sh.a.Cfg.UI.SidebarOpen {
		t.Fatal("sidebar toggle did not close")
	}
	sh.frame.Layout(fyne.NewSize(1280, 800))
	if sh.side.Object().Visible() {
		t.Fatal("sidebar visible after closing")
	}
	sh.toggleSidebar()
	sh.frame.Layout(fyne.NewSize(1280, 800))
	if !sh.a.Cfg.UI.SidebarOpen || !sh.side.Object().Visible() {
		t.Fatal("sidebar did not reopen")
	}

	sh.toggleInspector()
	if sh.a.Cfg.UI.InspectorOpen {
		t.Fatal("inspector toggle did not close")
	}
	sh.frame.Layout(fyne.NewSize(1280, 800))
	if sh.inspector.Object().Visible() {
		t.Fatal("inspector visible after closing")
	}
}

func TestNarrowWindowCollapsesPanels(t *testing.T) {
	sh := newTestShell(t)
	sh.frame.SetSidebar(true)
	sh.frame.SetInspector(true)

	sh.frame.Layout(fyne.NewSize(1280, 800))
	if !sh.side.Object().Visible() || !sh.inspector.Object().Visible() {
		t.Fatal("wide window hides panels")
	}
	sh.frame.Layout(fyne.NewSize(1001, 800))
	if !sh.side.Object().Visible() || !sh.inspector.Object().Visible() {
		t.Fatal("both panels should stay visible just above 1000px")
	}
	sh.frame.Layout(fyne.NewSize(999, 800))
	if !sh.side.Object().Visible() {
		t.Fatal("sidebar should stay visible at 999px")
	}
	if sh.inspector.Object().Visible() {
		t.Fatal("inspector should collapse below 1000px")
	}
	sh.frame.Layout(fyne.NewSize(819, 800))
	if sh.side.Object().Visible() {
		t.Fatal("sidebar should collapse below 820px")
	}
	// Preferences are untouched by the automatic collapse.
	if !sh.a.Cfg.UI.SidebarOpen || !sh.a.Cfg.UI.InspectorOpen {
		t.Fatal("auto-collapse changed the saved preference")
	}
	sh.frame.Layout(fyne.NewSize(1280, 800))
	if !sh.side.Object().Visible() || !sh.inspector.Object().Visible() {
		t.Fatal("panels did not return when the window grew")
	}
}

func TestPanelWidthsClamped(t *testing.T) {
	sh := newTestShell(t)
	sh.frame.sidebarW = 120
	sh.frame.Layout(fyne.NewSize(1280, 800))
	if sh.frame.sidebarW != 120 {
		t.Fatalf("layout mutated width: %v", sh.frame.sidebarW)
	}
	if got := clampPanel(100); got != 180 {
		t.Fatalf("clamp low = %v", got)
	}
	if got := clampPanel(999); got != 480 {
		t.Fatalf("clamp high = %v", got)
	}
}

func TestModeBadgeAndHeaderTitle(t *testing.T) {
	sh := newTestShell(t)
	sh.handleEvent(normalizeEvent(map[string]any{"type": "session.opened", "id": "s1",
		"title": "Fix the parser", "blocks": []any{}}))
	if got := sh.header.title.Text; got != "Fix the parser" {
		t.Fatalf("header title = %q", got)
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "mode.changed", "mode": "plan"}))
	if got := sh.header.badge.text; got != "PLAN" {
		t.Fatalf("badge = %q", got)
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "mode.changed", "mode": "default"}))
	if got := sh.header.badge.text; got != "BUILD" {
		t.Fatalf("badge = %q", got)
	}
}
