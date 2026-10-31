package main

import (
	"encoding/json"
	"net/url"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// showText is the shared read-only dialog: monospace body on a code surface
// with Copy and Close.
func (s *shell) showText(title, body string) {
	lbl := widget.NewLabel(body)
	lbl.Wrapping = fyne.TextWrapWord
	lbl.Selectable = true
	lbl.TextStyle = fyne.TextStyle{Monospace: true}
	content := newSurface(colorCode,
		container.New(&padLayout{l: spaceM, r: spaceM, t: spaceM, b: spaceM}, lbl)).
		border(colorBorder, 1)
	sc := container.NewVScroll(content)
	sc.SetMinSize(fyne.NewSize(680, 440))

	copyBtn := widget.NewButtonWithIcon("Copy", theme.ContentCopyIcon(), func() {
		s.win.Clipboard().SetContent(body)
		s.addNotice("✔ copied to clipboard")
	})
	copyBtn.Importance = widget.LowImportance
	bar := container.NewBorder(nil, nil, nil, copyBtn)
	d := dialog.NewCustom(title, "Close", container.NewBorder(nil, bar, nil, nil, sc), s.win)
	d.Resize(fyne.NewSize(720, 520))
	d.Show()
}

// showSettingsJSON is the advanced escape hatch: edit the whole config
// document with the same validation as the form.
func (s *shell) showSettingsJSON() {
	doc, err := s.b.configDoc()
	if err != nil {
		s.addNotice("⚠ settings: " + err.Error())
		return
	}
	b, err := json.MarshalIndent(json.RawMessage(doc), "", "  ")
	if err != nil {
		s.addNotice("⚠ settings: " + err.Error())
		return
	}
	ed := widget.NewMultiLineEntry()
	ed.SetText(string(b))
	ed.Wrapping = fyne.TextWrapOff
	ed.TextStyle = fyne.TextStyle{Monospace: true}
	sc := container.NewVScroll(ed)
	sc.SetMinSize(fyne.NewSize(760, 520))
	d := dialog.NewCustomConfirm("Configuration — ~/.anneal/config.json", "Save", "Cancel", sc,
		func(ok bool) {
			if !ok {
				return
			}
			if err := s.b.saveConfig(ed.Text); err != nil {
				s.addNotice("⚠ settings: " + err.Error())
				return
			}
			s.addNotice("✔ settings saved")
		}, s.win)
	d.Resize(fyne.NewSize(820, 600))
	d.Show()
}

// showCommands lists the slash commands (Help ▸ Commands).
func (s *shell) showCommands() {
	body := "Navigation\n" +
		"  /new /sessions /plan /build /stop\n" +
		"  /activity /runs /harness\n" +
		"Views\n" +
		"  /plan /build        switch permission mode\n" +
		"  /theme system|light|dark\n" +
		"  /textsize …         show or set the text size\n" +
		"Learning & memory\n" +
		"  /memory /skills /journey\n" +
		"  /learn /improve /index\n" +
		"Other\n" +
		"  /backend            show/choose the backend (restart to switch)\n" +
		"  /help /quit"
	s.showText("Commands", body)
}

// showShortcuts lists the keyboard shortcuts (Help ▸ Keyboard shortcuts).
func (s *shell) showShortcuts() {
	body := "Global\n" +
		"  Ctrl+N   New session\n" +
		"  Ctrl+B   Toggle sessions sidebar\n" +
		"  Ctrl+J   Toggle inspector\n" +
		"  Ctrl+1   Chat\n" +
		"  Ctrl+2   Harness\n" +
		"  Ctrl+3   Activity\n" +
		"  Ctrl+4   Settings\n" +
		"  Ctrl+,   Settings\n" +
		"  Ctrl+Q   Quit\n" +
		"Session\n" +
		"  Ctrl+M   Toggle plan/build mode\n" +
		"  Ctrl+D   Stop the current run\n" +
		"  Ctrl+I   Improve the harness\n" +
		"  Ctrl+L   Focus the composer\n" +
		"  Ctrl+R   Reload the harness pane\n" +
		"Display\n" +
		"  Ctrl+=   Bigger text\n" +
		"  Ctrl+-   Smaller text\n" +
		"  Ctrl+0   Reset text size\n" +
		"Composer\n" +
		"  Enter         Send\n" +
		"  Shift+Enter   New line"
	s.showText("Keyboard shortcuts", body)
}

// showAbout is the Help ▸ About dialog.
func (s *shell) showAbout() {
	title := widget.NewLabelWithStyle("Anneal", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	sub := widget.NewLabel("A self-improving desktop coding agent. Local model, local sessions, no cloud round-trips.")
	sub.Wrapping = fyne.TextWrapWord
	link, _ := url.Parse("https://github.com/uidbz/anneal")
	body := container.NewVBox(title, sub,
		widget.NewHyperlink("github.com/uidbz/anneal", link))
	d := dialog.NewCustom("About", "Close", body, s.win)
	d.Show()
}
