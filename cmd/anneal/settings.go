package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// settingsPage is the Settings view. The form (Phase 7) edits a copy of the
// config; the raw JSON editor stays available for advanced keys.
func (s *shell) settingsPage() fyne.CanvasObject {
	intro := widget.NewLabel("Settings edit ~/.anneal/config.json. Changes apply to the running session where possible; some integrations (tie, MCP) reconnect on restart.")
	intro.Wrapping = fyne.TextWrapWord
	jsonBtn := widget.NewButtonWithIcon("Edit configuration JSON…", theme.DocumentCreateIcon(), s.showSettingsJSON)
	jsonBtn.Importance = widget.HighImportance
	body := container.NewVBox(
		sectionHeading("Configuration"),
		intro,
		container.New(&padLayout{l: 0, r: 0, t: spaceS, b: 0}, jsonBtn),
	)
	return container.New(&padLayout{l: spaceXL, r: spaceXL, t: spaceL, b: spaceL},
		container.New(&maxWidthLayout{max: 760}, body))
}

// sectionHeading is a bold sub-heading inside pages and panels.
func sectionHeading(text string) *themedText {
	t := newThemedText(text, theme.ColorNameForeground)
	t.size = theme.SizeNameSubHeadingText
	t.bold = true
	return t
}

// settingsPanel is the Settings form (Phase 7). Until the form lands, the
// page only offers the JSON editor.
type settingsPanel struct {
	s *shell
}

func (p *settingsPanel) reload()                         {}
func (p *settingsPanel) onExternalChange(map[string]any) {}
