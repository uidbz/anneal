package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/uidbz/anneal/core/config"
)

// textScaleTheme scales the theme's text sizes (and inline icons, so they
// track the text they sit beside) while delegating colors, fonts, icons, and
// all other sizes to the wrapped theme. Fyne widgets have no per-widget font
// size, so the theme is the only way to control text size app-wide.
type textScaleTheme struct {
	fyne.Theme
	scale float32
}

// scaledTextSizes are the theme sizes the text scale multiplies.
var scaledTextSizes = map[fyne.ThemeSizeName]bool{
	theme.SizeNameText:           true,
	theme.SizeNameCaptionText:    true,
	theme.SizeNameHeadingText:    true,
	theme.SizeNameSubHeadingText: true,
	theme.SizeNameInlineIcon:     true,
}

func (t *textScaleTheme) Size(name fyne.ThemeSizeName) float32 {
	if scaledTextSizes[name] {
		return t.Theme.Size(name) * t.scale
	}
	return t.Theme.Size(name)
}

// applyTextScale installs the configured text scale on the Fyne app. base must
// be the theme captured before the first scale was applied; wrapping an
// already-wrapped theme would compound the scale on every config change. At
// 1.0 the base theme is used untouched; SetTheme repaints live.
func applyTextScale(app fyne.App, base fyne.Theme, scale float64) {
	scale = config.ClampTextScale(scale)
	if scale == 1.0 {
		app.Settings().SetTheme(base)
		return
	}
	app.Settings().SetTheme(&textScaleTheme{Theme: base, scale: float32(scale)})
}
