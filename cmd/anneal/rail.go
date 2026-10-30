package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Rail geometry (px at 100%). The rail holds one icon + caption per view.
const (
	railWidth    float32 = 60
	railItemH    float32 = 54
	railIconSize float32 = 20
	handleWidth  float32 = 5
	headerHeight float32 = 46
	statusBarH   float32 = 28
)

// railItem is one view button in the activity rail: icon above a small
// caption, with a left accent bar and a raised background when active.
type railItem struct {
	widget.BaseWidget

	icon    fyne.Resource
	caption string
	active  bool
	hover   bool
	onTap   func()
}

func newRailItem(icon fyne.Resource, caption string, onTap func()) *railItem {
	r := &railItem{icon: icon, caption: caption, onTap: onTap}
	r.ExtendBaseWidget(r)
	return r
}

func (r *railItem) SetActive(active bool) {
	if r.active == active {
		return
	}
	r.active = active
	r.Refresh()
}

func (r *railItem) Tapped(*fyne.PointEvent) {
	if r.onTap != nil {
		r.onTap()
	}
}

func (r *railItem) MouseIn(*desktop.MouseEvent) {
	r.hover = true
	r.Refresh()
}

func (r *railItem) MouseMoved(*desktop.MouseEvent) {}

func (r *railItem) MouseOut() {
	r.hover = false
	r.Refresh()
}

func (r *railItem) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (r *railItem) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(theme.Color(colorRaised))
	bg.CornerRadius = radiusCard
	bar := canvas.NewRectangle(theme.Color(theme.ColorNamePrimary))
	bar.CornerRadius = 2
	icon := canvas.NewImageFromResource(r.icon)
	icon.FillMode = canvas.ImageFillContain
	label := canvas.NewText(r.caption, theme.Color(colorMuted))
	label.Alignment = fyne.TextAlignCenter
	return &railItemRenderer{owner: r, bg: bg, bar: bar, icon: icon, label: label,
		objects: []fyne.CanvasObject{bg, bar, icon, label}}
}

type railItemRenderer struct {
	owner   *railItem
	bg      *canvas.Rectangle
	bar     *canvas.Rectangle
	icon    *canvas.Image
	label   *canvas.Text
	objects []fyne.CanvasObject
}

func (r *railItemRenderer) Layout(size fyne.Size) {
	r.bg.Move(fyne.NewPos(spaceXS, 2))
	r.bg.Resize(fyne.NewSize(size.Width-2*spaceXS, size.Height-4))
	r.bar.Move(fyne.NewPos(0, size.Height*0.2))
	r.bar.Resize(fyne.NewSize(3, size.Height*0.6))
	iconY := float32(8)
	r.icon.Move(fyne.NewPos((size.Width-railIconSize)/2, iconY))
	r.icon.Resize(fyne.NewSize(railIconSize, railIconSize))
	labelY := iconY + railIconSize + 3
	r.label.Move(fyne.NewPos(0, labelY))
	r.label.TextSize = theme.Size(theme.SizeNameCaptionText)
	r.label.Resize(fyne.NewSize(size.Width, r.label.TextSize+2))
}

func (r *railItemRenderer) MinSize() fyne.Size {
	return fyne.NewSize(railWidth, railItemH)
}

func (r *railItemRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *railItemRenderer) Refresh() {
	o := r.owner
	switch {
	case o.active:
		r.bg.FillColor = theme.Color(colorRaised)
		r.bar.FillColor = theme.Color(theme.ColorNamePrimary)
		r.icon.Resource = theme.NewColoredResource(o.icon, theme.ColorNamePrimary)
		r.label.Color = theme.Color(theme.ColorNameForeground)
	case o.hover:
		r.bg.FillColor = theme.Color(theme.ColorNameHover)
		r.bar.FillColor = theme.Color(colorRail)
		r.icon.Resource = theme.NewColoredResource(o.icon, theme.ColorNameForeground)
		r.label.Color = theme.Color(theme.ColorNameForeground)
	default:
		r.bg.FillColor = theme.Color(colorRail)
		r.bar.FillColor = theme.Color(colorRail)
		r.icon.Resource = theme.NewColoredResource(o.icon, colorMuted)
		r.label.Color = theme.Color(colorMuted)
	}
	r.icon.Refresh()
	r.bg.Refresh()
	r.bar.Refresh()
	r.label.Refresh()
}

func (r *railItemRenderer) Destroy() {}

// rail is the fixed-width activity rail. Settings sits at the bottom.
type rail struct {
	widget.BaseWidget
	top     *fyne.Container
	bottom  *fyne.Container
	bg      *canvas.Rectangle
	logo    *canvas.Image
	content fyne.CanvasObject
}

func newRail(views []fyne.CanvasObject, settings fyne.CanvasObject, logo fyne.Resource) *rail {
	r := &rail{bg: canvas.NewRectangle(theme.Color(colorRail))}
	r.ExtendBaseWidget(r)
	items := container.New(&columnLayout{gap: spaceXS})
	for _, v := range views {
		items.Add(v)
	}
	r.top = container.New(&padLayout{l: 0, r: 0, t: spaceS, b: 0}, items)
	r.bottom = container.New(&padLayout{l: 0, r: 0, t: 0, b: spaceS},
		container.New(&columnLayout{gap: spaceXS}, settings))
	r.logo = canvas.NewImageFromResource(logo)
	r.logo.FillMode = canvas.ImageFillContain
	r.content = container.NewBorder(
		newLogoMark(logo), r.bottom, nil, nil, r.top)
	return r
}

func (r *rail) CreateRenderer() fyne.WidgetRenderer {
	return &railRenderer{rail: r, objects: []fyne.CanvasObject{r.bg, r.content}}
}

type railRenderer struct {
	rail    *rail
	objects []fyne.CanvasObject
}

func (r *railRenderer) Layout(size fyne.Size) {
	r.rail.bg.Move(fyne.NewPos(0, 0))
	r.rail.bg.Resize(size)
	r.rail.logo.Resize(fyne.NewSize(28, 28))
	r.rail.content.Move(fyne.NewPos(0, 0))
	r.rail.content.Resize(size)
}

func (r *railRenderer) MinSize() fyne.Size {
	return fyne.NewSize(railWidth, r.rail.content.MinSize().Height)
}

func (r *railRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *railRenderer) Refresh() {
	r.rail.bg.FillColor = theme.Color(colorRail)
	r.rail.bg.Refresh()
}

func (r *railRenderer) Destroy() {}

// logoMark draws the brand mark at a fixed size (canvas.Image reports a 1px
// minimum, which would collapse inside a border slot).
type logoMark struct {
	widget.BaseWidget
	img *canvas.Image
}

func newLogoMark(res fyne.Resource) *logoMark {
	l := &logoMark{img: canvas.NewImageFromResource(res)}
	l.img.FillMode = canvas.ImageFillContain
	l.ExtendBaseWidget(l)
	return l
}

func (l *logoMark) MinSize() fyne.Size { return fyne.NewSize(railWidth, 40) }

func (l *logoMark) CreateRenderer() fyne.WidgetRenderer {
	return &logoMarkRenderer{mark: l, img: l.img}
}

type logoMarkRenderer struct {
	mark *logoMark
	img  *canvas.Image
}

func (r *logoMarkRenderer) Layout(size fyne.Size) {
	const side float32 = 26
	r.img.Move(fyne.NewPos((size.Width-side)/2, (size.Height-side)/2))
	r.img.Resize(fyne.NewSize(side, side))
}

func (r *logoMarkRenderer) MinSize() fyne.Size { return r.mark.MinSize() }

func (r *logoMarkRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.img} }

func (r *logoMarkRenderer) Refresh() { r.img.Refresh() }

func (r *logoMarkRenderer) Destroy() {}
