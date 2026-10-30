package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Spacing scale (px at 100% text size). The shell uses these instead of
// scattering literal paddings.
const (
	spaceXS float32 = 4
	spaceS  float32 = 8
	spaceM  float32 = 12
	spaceL  float32 = 16
	spaceXL float32 = 24

	radiusCard float32 = 8
	radiusPill float32 = 999
)

// surface is a themed rounded panel: a fill from a palette token, an optional
// hairline border, padding, and exactly one child. Colors are re-read on every
// refresh, so a light/dark switch repaints it along with the stock widgets.
type surface struct {
	widget.BaseWidget

	fill      fyne.ThemeColorName
	stroke    fyne.ThemeColorName
	strokeW   float32
	radius    float32
	pad       float32
	padTop    float32
	padLeft   float32
	padRight  float32
	padBottom float32
	content   fyne.CanvasObject
}

func newSurface(fill fyne.ThemeColorName, content fyne.CanvasObject) *surface {
	s := &surface{fill: fill, radius: radiusCard, pad: spaceM, content: content}
	s.ExtendBaseWidget(s)
	return s
}

// padding overrides the uniform inset.
func (s *surface) padding(l, t, r, b float32) *surface {
	s.padLeft, s.padTop, s.padRight, s.padBottom = l, t, r, b
	return s
}

// border draws a hairline (or thicker) stroke in the given token.
func (s *surface) border(name fyne.ThemeColorName, width float32) *surface {
	s.stroke, s.strokeW = name, width
	return s
}

func (s *surface) CreateRenderer() fyne.WidgetRenderer {
	rect := canvas.NewRectangle(color.Transparent)
	rect.CornerRadius = s.radius
	return &surfaceRenderer{surf: s, rect: rect, objects: []fyne.CanvasObject{rect, s.content}}
}

type surfaceRenderer struct {
	surf    *surface
	rect    *canvas.Rectangle
	objects []fyne.CanvasObject
}

func (r *surfaceRenderer) Layout(size fyne.Size) {
	r.rect.Move(fyne.NewPos(0, 0))
	r.rect.Resize(size)
	s := r.surf
	x, y := s.padLeft, s.padTop
	w := size.Width - s.padLeft - s.padRight
	h := size.Height - s.padTop - s.padBottom
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	s.content.Move(fyne.NewPos(x, y))
	s.content.Resize(fyne.NewSize(w, h))
}

func (r *surfaceRenderer) MinSize() fyne.Size {
	s := r.surf
	ms := s.content.MinSize()
	return fyne.NewSize(
		ms.Width+s.padLeft+s.padRight+s.strokeW*2,
		ms.Height+s.padTop+s.padBottom+s.strokeW*2)
}

func (r *surfaceRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *surfaceRenderer) Refresh() {
	s := r.surf
	r.rect.FillColor = theme.Color(s.fill)
	r.rect.CornerRadius = s.radius
	if s.stroke != "" && s.strokeW > 0 {
		r.rect.StrokeColor = theme.Color(s.stroke)
		r.rect.StrokeWidth = s.strokeW
	} else {
		r.rect.StrokeColor = color.Transparent
		r.rect.StrokeWidth = 0
	}
	r.rect.Refresh()
}

func (r *surfaceRenderer) Destroy() {}

// card wraps content in the standard raised card surface.
func card(content fyne.CanvasObject) *surface {
	return newSurface(colorRaised, content).border(colorBorder, 1)
}

// pill draws a small rounded badge (counts, modes, kinds) with a wash fill.
func pill(text string, fill fyne.ThemeColorName, token fyne.ThemeColorName) fyne.CanvasObject {
	t := newThemedText(text, token)
	t.bold = true
	t.align = fyne.TextAlignCenter
	return newSurface(fill, t).padding(spaceS, 1, spaceS, 1).border(colorBorder, 1)
}

// themedText is compact chrome text (status bar, rail captions, badges) that
// follows the theme and the text scale. Labels carry selection and wrapping;
// this is for the one-line bits where those would add height or interfere
// with custom layout.
type themedText struct {
	widget.BaseWidget

	text  string
	token fyne.ThemeColorName
	bold  bool
	mono  bool
	size  fyne.ThemeSizeName
	align fyne.TextAlign
}

func newThemedText(text string, token fyne.ThemeColorName) *themedText {
	t := &themedText{text: text, token: token, size: theme.SizeNameCaptionText, align: fyne.TextAlignLeading}
	t.ExtendBaseWidget(t)
	return t
}

func (t *themedText) SetText(s string) {
	if t.text == s {
		return
	}
	t.text = s
	t.Refresh()
}

func (t *themedText) Text() string { return t.text }

func (t *themedText) CreateRenderer() fyne.WidgetRenderer {
	ct := canvas.NewText(t.text, color.Transparent)
	return &themedTextRenderer{owner: t, text: ct, objects: []fyne.CanvasObject{ct}}
}

type themedTextRenderer struct {
	owner   *themedText
	text    *canvas.Text
	objects []fyne.CanvasObject
}

func (r *themedTextRenderer) Layout(size fyne.Size) { r.text.Resize(r.text.MinSize()) }

func (r *themedTextRenderer) MinSize() fyne.Size { return r.text.MinSize() }

func (r *themedTextRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *themedTextRenderer) Refresh() {
	o := r.owner
	r.text.Text = o.text
	r.text.Color = theme.Color(o.token)
	r.text.TextSize = theme.Size(o.size)
	r.text.TextStyle = fyne.TextStyle{Bold: o.bold, Monospace: o.mono}
	r.text.Alignment = o.align
	r.text.Refresh()
}

func (r *themedTextRenderer) Destroy() {}

// divider is a hairline in the border token. horizontal spans its width;
// vertical spans its height (it needs a caller-set minimum size).
type divider struct {
	widget.BaseWidget
	vertical bool
}

func newDivider() *divider {
	d := &divider{}
	d.ExtendBaseWidget(d)
	return d
}

func newVDivider() *divider {
	d := &divider{vertical: true}
	d.ExtendBaseWidget(d)
	return d
}

func (d *divider) CreateRenderer() fyne.WidgetRenderer {
	rect := canvas.NewRectangle(color.Transparent)
	return &dividerRenderer{div: d, rect: rect}
}

type dividerRenderer struct {
	div  *divider
	rect *canvas.Rectangle
}

func (r *dividerRenderer) Layout(size fyne.Size) {
	r.rect.Move(fyne.NewPos(0, 0))
	r.rect.Resize(size)
}

func (r *dividerRenderer) MinSize() fyne.Size {
	if r.div.vertical {
		return fyne.NewSize(1, 0)
	}
	return fyne.NewSize(0, 1)
}

func (r *dividerRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.rect} }

func (r *dividerRenderer) Refresh() {
	r.rect.FillColor = theme.Color(colorBorder)
	r.rect.Refresh()
}

func (r *dividerRenderer) Destroy() {}

// meter is a thin progress bar (context usage, task completion) whose fill
// switches from accent to warn to err by threshold and follows the theme.
type meter struct {
	widget.BaseWidget
	value  float64 // 0..1
	height float32
}

func newMeter(value float64, height float32) *meter {
	m := &meter{value: value, height: height}
	m.ExtendBaseWidget(m)
	return m
}

func (m *meter) SetValue(v float64) {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	m.value = v
	m.Refresh()
}

func (m *meter) Value() float64 { return m.value }

func (m *meter) CreateRenderer() fyne.WidgetRenderer {
	track := canvas.NewRectangle(color.Transparent)
	track.CornerRadius = m.height / 2
	fill := canvas.NewRectangle(color.Transparent)
	fill.CornerRadius = m.height / 2
	return &meterRenderer{meter: m, track: track, fill: fill,
		objects: []fyne.CanvasObject{track, fill}}
}

type meterRenderer struct {
	meter   *meter
	track   *canvas.Rectangle
	fill    *canvas.Rectangle
	objects []fyne.CanvasObject
}

func (r *meterRenderer) Layout(size fyne.Size) {
	r.track.Move(fyne.NewPos(0, 0))
	r.track.Resize(size)
	w := float32(r.meter.value) * size.Width
	if w < r.meter.height && r.meter.value > 0 {
		w = r.meter.height
	}
	r.fill.Move(fyne.NewPos(0, 0))
	r.fill.Resize(fyne.NewSize(w, size.Height))
}

func (r *meterRenderer) MinSize() fyne.Size {
	return fyne.NewSize(spaceXL*2, r.meter.height)
}

func (r *meterRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *meterRenderer) Refresh() {
	token := colorOK
	switch {
	case r.meter.value >= 0.82:
		token = colorErr
	case r.meter.value >= 0.70:
		token = colorWarn
	case r.meter.value > 0:
		token = theme.ColorNamePrimary
	}
	r.track.FillColor = theme.Color(colorBorder)
	r.fill.FillColor = theme.Color(token)
	r.track.Refresh()
	r.fill.Refresh()
}

func (r *meterRenderer) Destroy() {}

// --- layouts ---------------------------------------------------------------

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

// maxWidthLayout centers one child and caps its width, so prose keeps a
// readable measure on wide windows.
type maxWidthLayout struct {
	max float32
}

func (l *maxWidthLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	if len(objects) == 0 {
		return
	}
	w := size.Width
	if l.max > 0 && w > l.max {
		w = l.max
	}
	x := (size.Width - w) / 2
	for _, o := range objects {
		o.Move(fyne.NewPos(x, 0))
		o.Resize(fyne.NewSize(w, size.Height))
	}
}

func (l *maxWidthLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var ms fyne.Size
	for _, o := range objects {
		s := o.MinSize()
		if s.Width > ms.Width {
			ms.Width = s.Width
		}
		if s.Height > ms.Height {
			ms.Height = s.Height
		}
	}
	if l.max > 0 && ms.Width > l.max {
		ms.Width = l.max
	}
	return ms
}

// hgap is a transparent horizontal spacer (Fyne box layouts have no item
// spacing).
func hgap(w float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(w, 0))
	return r
}

// vgap is a transparent vertical spacer.
func vgap(h float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(0, h))
	return r
}

// spacer absorbs slack in a box layout (transparent, zero minimum size).
func spacer() fyne.CanvasObject {
	return canvas.NewRectangle(color.Transparent)
}
