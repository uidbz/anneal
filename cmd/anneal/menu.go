package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// shortcut builds a Ctrl+key shortcut.
func shortcut(key fyne.KeyName) *desktop.CustomShortcut {
	return &desktop.CustomShortcut{KeyName: key, Modifier: fyne.KeyModifierControl}
}

// installMenu attaches the native main menu. Menu shortcuts fire regardless
// of which widget has focus (the GLFW driver checks the menu first), so the
// menu is the single source of truth for global keys.
func (s *shell) installMenu() {
	item := func(label string, sc *desktop.CustomShortcut, fn func()) *fyne.MenuItem {
		mi := fyne.NewMenuItem(label, fn)
		if sc != nil {
			mi.Shortcut = sc
		}
		return mi
	}

	quit := item("Quit", shortcut(fyne.KeyQ), func() { s.app.Quit() })
	quit.IsQuit = true

	file := fyne.NewMenu("File",
		item("New session", shortcut(fyne.KeyN), func() { s.a.Runner.NewSession() }),
		item("Settings…", shortcut(fyne.KeyComma), s.showSettings),
		fyne.NewMenuItemSeparator(),
		quit,
	)

	view := fyne.NewMenu("View",
		item("Chat", shortcut(fyne.Key1), func() { s.showView(viewChat) }),
		item("Harness", shortcut(fyne.Key2), func() { s.showView(viewHarness) }),
		item("Activity", shortcut(fyne.Key3), func() { s.showView(viewActivity) }),
		item("Settings", shortcut(fyne.Key4), func() { s.showView(viewSettings) }),
		fyne.NewMenuItemSeparator(),
		item("Toggle sessions sidebar", shortcut(fyne.KeyB), s.toggleSidebar),
		item("Toggle inspector", shortcut(fyne.KeyJ), s.toggleInspector),
		fyne.NewMenuItemSeparator(),
		item("Bigger text", shortcut(fyne.KeyEqual), func() { s.nudgeTextScale(textScaleStep) }),
		item("Smaller text", shortcut(fyne.KeyMinus), func() { s.nudgeTextScale(-textScaleStep) }),
		item("Reset text size", shortcut(fyne.Key0), func() { s.setTextScale(1.0) }),
		fyne.NewMenuItemSeparator(),
		s.themeMenu(),
	)

	session := fyne.NewMenu("Session",
		item("Toggle plan/build mode", shortcut(fyne.KeyM), func() {
			if s.mode == "plan" {
				s.a.Runner.SetMode("default")
			} else {
				s.a.Runner.SetMode("plan")
			}
		}),
		item("Stop", shortcut(fyne.KeyD), func() { s.a.Runner.Interrupt() }),
		fyne.NewMenuItemSeparator(),
		item("Improve harness", shortcut(fyne.KeyI), s.improveNow),
		item("Learn now", nil, func() { s.a.Review.LearnNow(); s.addNotice("Learning pass queued") }),
		item("Rebuild search index", nil, func() { s.slash("/index") }),
		fyne.NewMenuItemSeparator(),
		item("Focus composer", shortcut(fyne.KeyL), func() { s.win.Canvas().Focus(s.entry) }),
		item("Reload harness pane", shortcut(fyne.KeyR), s.reloadPane),
	)

	help := fyne.NewMenu("Help",
		item("Commands", nil, func() { s.showCommands() }),
		item("Keyboard shortcuts", nil, func() { s.showShortcuts() }),
		item("About Anneal", nil, s.showAbout),
	)

	s.win.SetMainMenu(fyne.NewMainMenu(file, view, session, help))

	// Aliases that a menu item cannot express (Ctrl+= is the primary binding).
	s.win.Canvas().AddShortcut(shortcut(fyne.KeyPlus), func(fyne.Shortcut) {
		s.nudgeTextScale(textScaleStep)
	})
}

// themeMenu is the View ▸ Theme submenu.
func (s *shell) themeMenu() *fyne.MenuItem {
	themeItem := fyne.NewMenuItem("Theme", nil)
	themeItem.ChildMenu = fyne.NewMenu("",
		fyne.NewMenuItem("System", func() { s.setThemeMode("system") }),
		fyne.NewMenuItem("Light", func() { s.setThemeMode("light") }),
		fyne.NewMenuItem("Dark", func() { s.setThemeMode("dark") }),
	)
	return themeItem
}

// showOverflow opens the header's "more" menu under the button.
func (s *shell) showOverflow() {
	item := func(label string, fn func()) *fyne.MenuItem { return fyne.NewMenuItem(label, fn) }
	menu := fyne.NewMenu("",
		item("New session", func() { s.a.Runner.NewSession() }),
		item("Rebuild search index", func() { s.slash("/index") }),
		item("Learn now", func() { s.a.Review.LearnNow(); s.addNotice("Learning pass queued") }),
		fyne.NewMenuItemSeparator(),
		item("Memory…", func() { s.slash("/memory") }),
		item("Skills…", func() { s.slash("/skills") }),
		item("Journey…", func() { s.slash("/journey") }),
		fyne.NewMenuItemSeparator(),
		item("Reload harness pane", s.reloadPane),
		item("Settings", s.showSettings),
	)
	c := s.win.Canvas()
	sz := c.Size()
	widget.ShowPopUpMenuAtPosition(menu, c, fyne.NewPos(sz.Width-280, 64))
}
