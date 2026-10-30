package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// header is a view's top bar: title, optional badge/subtitle, actions on the
// right, and a hairline underneath.
type header struct {
	title    *widget.Label // ellipsized so a long title never collides with actions
	subtitle *themedText
	badge    *badge
	actions  *fyne.Container
	object   fyne.CanvasObject
}

func newHeader(title string) *header {
	h := &header{
		title:    widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		subtitle: newThemedText("", colorMuted),
		badge:    newBadge("", colorRaised, colorMuted),
		actions:  container.NewHBox(),
	}
	h.title.SizeName = theme.SizeNameSubHeadingText
	h.title.Truncation = fyne.TextTruncateEllipsis
	h.title.Wrapping = fyne.TextWrapOff

	center := container.New(&columnLayout{gap: 0}, h.title, h.subtitle)
	right := container.New(&padLayout{l: spaceS, r: 0, t: 0, b: 0},
		container.New(&rowLayout{gap: spaceS}, h.badge, h.actions))
	row := container.NewBorder(nil, nil, nil, right, center)
	h.object = container.NewVBox(
		container.New(&padLayout{l: spaceL, r: spaceM, t: spaceS, b: spaceS}, row),
		newDivider(),
	)
	return h
}

func (h *header) setTitle(text string) {
	h.title.SetText(text)
	h.object.Refresh()
}

func (h *header) setSubtitle(text string) {
	h.subtitle.SetText(text)
	h.object.Refresh()
}

func (h *header) setBadge(text string, fill, fg fyne.ThemeColorName) {
	h.badge.SetText(text)
	h.badge.fill, h.badge.fg = fill, fg
	h.badge.Refresh()
}

func (h *header) setActions(objects ...fyne.CanvasObject) {
	h.actions.Objects = objects
	h.actions.Refresh()
}

// badge is a small bold pill (BUILD / PLAN, counts) whose colors follow the
// theme. An empty text collapses it to zero size.
type badge struct {
	widget.BaseWidget
	text string
	fill fyne.ThemeColorName
	fg   fyne.ThemeColorName
}

func newBadge(text string, fill, fg fyne.ThemeColorName) *badge {
	b := &badge{text: text, fill: fill, fg: fg}
	b.ExtendBaseWidget(b)
	return b
}

func (b *badge) SetText(text string) {
	b.text = text
	b.Refresh()
}

func (b *badge) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(theme.Color(colorRaised))
	bg.CornerRadius = 4
	text := canvas.NewText("", theme.Color(colorMuted))
	text.TextStyle = fyne.TextStyle{Bold: true}
	text.Alignment = fyne.TextAlignCenter
	return &badgeRenderer{owner: b, bg: bg, text: text,
		objects: []fyne.CanvasObject{bg, text}}
}

type badgeRenderer struct {
	owner   *badge
	bg      *canvas.Rectangle
	text    *canvas.Text
	objects []fyne.CanvasObject
}

func (r *badgeRenderer) Layout(size fyne.Size) {
	r.bg.Move(fyne.NewPos(0, 0))
	r.bg.Resize(size)
	r.text.Move(fyne.NewPos(0, (size.Height-r.text.MinSize().Height)/2))
	r.text.Resize(fyne.NewSize(size.Width, r.text.MinSize().Height))
}

func (r *badgeRenderer) MinSize() fyne.Size {
	if r.owner.text == "" {
		return fyne.Size{}
	}
	r.text.TextSize = theme.Size(theme.SizeNameCaptionText)
	ms := r.text.MinSize()
	return fyne.NewSize(ms.Width+spaceS*1.5, ms.Height+2)
}

func (r *badgeRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *badgeRenderer) Refresh() {
	b := r.owner
	r.bg.FillColor = theme.Color(b.fill)
	r.bg.CornerRadius = 4
	r.text.Text = b.text
	r.text.Color = theme.Color(b.fg)
	r.text.TextSize = theme.Size(theme.SizeNameCaptionText)
	r.text.TextStyle = fyne.TextStyle{Bold: true}
	r.bg.Refresh()
	r.text.Refresh()
}

func (r *badgeRenderer) Destroy() {}

// iconButton is a flat, icon-only header action.
func iconButton(icon fyne.Resource, fn func()) *widget.Button {
	b := widget.NewButtonWithIcon("", icon, fn)
	b.Importance = widget.LowImportance
	return b
}

// labeledButton is a flat header action with a short icon + label.
func labeledButton(label string, icon fyne.Resource, fn func()) *widget.Button {
	b := widget.NewButtonWithIcon(label, icon, fn)
	b.Importance = widget.LowImportance
	return b
}
