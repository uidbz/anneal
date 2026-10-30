package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/uidbz/anneal/core/config"
)

// Brand palette. The dark Fyne theme is kept as the base; the teal accent
// drives focus rings, selections, the tab indicator, and the status dots so
// the window reads as one designed surface rather than default widgets.
var (
	accent      = color.NRGBA{R: 0x2d, G: 0xd4, B: 0xbf, A: 0xff} // teal
	okColor     = color.NRGBA{R: 0x4a, G: 0xde, B: 0x80, A: 0xff} // online/ready
	warnColor   = color.NRGBA{R: 0xf5, G: 0x9e, B: 0x0b, A: 0xff} // connecting/busy
	errColor    = color.NRGBA{R: 0xef, G: 0x44, B: 0x44, A: 0xff} // offline/error
	idleColor   = color.NRGBA{R: 0x6b, G: 0x72, B: 0x80, A: 0xff} // unknown/idle
	accentSoft  = color.NRGBA{R: 0x2d, G: 0xd4, B: 0xbf, A: 0x33}
	selectionBg = color.NRGBA{R: 0x2d, G: 0xd4, B: 0xbf, A: 0x26}
)

// brandTheme overrides the accent-bearing theme colors on top of the app's
// base theme and delegates everything else unchanged.
type brandTheme struct {
	fyne.Theme
}

func (t *brandTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNamePrimary:
		return accent
	case theme.ColorNameFocus:
		return accent
	case theme.ColorNameSelection:
		return selectionBg
	case theme.ColorNameHover:
		return accentSoft
	}
	return t.Theme.Color(name, variant)
}

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

// applyTextScale installs the configured text scale on the Fyne app. base is
// the theme captured before the first scale was applied; wrapping an
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

// statusDot is the small colored circle that turns a status label into an
// at-a-glance chip: green = online, amber = busy/connecting, red = offline,
// gray = unknown/idle.
type statusDot struct {
	widget.BaseWidget
	col color.Color
}

func newStatusDot() *statusDot {
	d := &statusDot{col: idleColor}
	d.ExtendBaseWidget(d)
	return d
}

// setStatus repaints the dot with a state color.
func (d *statusDot) setStatus(c color.Color) {
	d.col = c
	d.Refresh()
}

func (d *statusDot) CreateRenderer() fyne.WidgetRenderer {
	return &statusDotRenderer{dot: d, circle: canvas.NewCircle(d.col)}
}

type statusDotRenderer struct {
	dot    *statusDot
	circle *canvas.Circle
}

func (r *statusDotRenderer) Layout(size fyne.Size) {
	r.circle.Move(fyne.NewPos(0, 0))
	r.circle.Resize(size)
}

func (r *statusDotRenderer) MinSize() fyne.Size { return fyne.NewSize(10, 10) }

func (r *statusDotRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.circle}
}

func (r *statusDotRenderer) Refresh() {
	r.circle.FillColor = r.dot.col
	r.circle.Refresh()
}

func (r *statusDotRenderer) Destroy() {}

// padLayout insets its children as a group (the transcript needs breathing
// room from the window edges; Fyne has no padding container).
type padLayout struct {
	l, r, t, b float32
}

func (l *padLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Move(fyne.NewPos(l.l, l.t))
		o.Resize(fyne.NewSize(size.Width-l.l-l.r, size.Height-l.t-l.b))
	}
}

func (l *padLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var w, h float32
	for _, o := range objects {
		ms := o.MinSize()
		if ms.Width > w {
			w = ms.Width
		}
		h += ms.Height
	}
	return fyne.NewSize(w+l.l+l.r, h+l.t+l.b)
}

// rowLayout packs children left-to-right at their MinSize widths with a
// fixed gap (a transparent trailing spacer absorbs any slack). Fyne's box
// layouts distribute slack across children, which stretches dots and
// collides label metrics, so header/status rows use this instead.
type rowLayout struct {
	gap float32
}

func (l *rowLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	x := float32(0)
	for i, o := range objects {
		if i > 0 {
			x += l.gap
		}
		ms := o.MinSize()
		o.Move(fyne.NewPos(x, (size.Height-ms.Height)/2))
		o.Resize(fyne.NewSize(ms.Width, ms.Height))
		x += ms.Width
	}
}

func (l *rowLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var w, h float32
	for i, o := range objects {
		if i > 0 {
			w += l.gap
		}
		ms := o.MinSize()
		w += ms.Width
		if ms.Height > h {
			h = ms.Height
		}
	}
	return fyne.NewSize(w, h)
}
