package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Auto-collapse thresholds (window width, px). The saved user preference is
// untouched; the panels reappear when the window grows again.
const (
	narrowSidebarW   float32 = 900
	narrowInspectorW float32 = 1180
)

// chromeframe lays out the sidebar, the main column, and the inspector around
// a fixed rail, with draggable split handles between them.
type chromeframe struct {
	widget.BaseWidget

	sidebar   fyne.CanvasObject
	main      fyne.CanvasObject
	inspector fyne.CanvasObject

	sideHandle *splitHandle
	inspHandle *splitHandle

	sidebarW      float32
	inspectorW    float32
	sidebarPref   bool
	inspectorPref bool
	panels        bool // panels belong to the active view (chat)

	sidebarShown   bool
	inspectorShown bool

	onPanelResize func(sidebar bool, width float32)
	onWidth       func(width float32)
	lastWidth     float32
}

func newChromeFrame(sidebar, main, inspector fyne.CanvasObject, sidebarW, inspectorW float32) *chromeframe {
	f := &chromeframe{
		sidebar: sidebar, main: main, inspector: inspector,
		sidebarW: sidebarW, inspectorW: inspectorW,
		sidebarPref: true, inspectorPref: true, panels: true,
	}
	f.sideHandle = newSplitHandle(false, func(dx float32) {
		f.sidebarW = clampPanel(f.sidebarW + dx)
		f.relayout()
	}, func() {
		if f.onPanelResize != nil {
			f.onPanelResize(true, f.sidebarW)
		}
	})
	f.inspHandle = newSplitHandle(false, func(dx float32) {
		f.inspectorW = clampPanel(f.inspectorW - dx)
		f.relayout()
	}, func() {
		if f.onPanelResize != nil {
			f.onPanelResize(false, f.inspectorW)
		}
	})
	f.ExtendBaseWidget(f)
	return f
}

func clampPanel(w float32) float32 {
	if w < 180 {
		return 180
	}
	if w > 480 {
		return 480
	}
	return w
}

func (f *chromeframe) SetSidebar(pref bool) {
	f.sidebarPref = pref
	f.relayout()
}

func (f *chromeframe) SetInspector(pref bool) {
	f.inspectorPref = pref
	f.relayout()
}

// SetPanels controls whether the sidebar/inspector belong to the active view.
func (f *chromeframe) SetPanels(on bool) {
	if f.panels == on {
		return
	}
	f.panels = on
	f.relayout()
}

func (f *chromeframe) relayout() {
	f.Resize(f.Size())
	f.Refresh()
}

// showPanels computes visibility for the current width: a hidden preference
// always hides; otherwise narrow windows collapse the panels temporarily.
func (f *chromeframe) showPanels(size fyne.Size) (bool, bool) {
	return f.panels && f.sidebarPref && size.Width >= narrowSidebarW,
		f.panels && f.inspectorPref && size.Width >= narrowInspectorW
}

func (f *chromeframe) Layout(size fyne.Size) {
	if f.onWidth != nil && (size.Width < 1000) != (f.lastWidth < 1000) {
		f.onWidth(size.Width)
	}
	f.lastWidth = size.Width
	sidebar, inspector := f.showPanels(size)
	if sidebar != f.sidebarShown {
		f.sidebarShown = sidebar
		toggleShown(f.sidebar, sidebar)
		toggleShown(f.sideHandle, sidebar)
		if !sidebar && f.inspHandle != nil {
			// keep the inspector handle visible independently
		}
	}
	if inspector != f.inspectorShown {
		f.inspectorShown = inspector
		toggleShown(f.inspector, inspector)
		toggleShown(f.inspHandle, inspector)
	}

	x := float32(0)
	if sidebar {
		f.sidebar.Move(fyne.NewPos(x, 0))
		f.sidebar.Resize(fyne.NewSize(f.sidebarW, size.Height))
		x += f.sidebarW
		f.sideHandle.Move(fyne.NewPos(x, 0))
		f.sideHandle.Resize(fyne.NewSize(handleWidth, size.Height))
		x += handleWidth
	}

	right := size.Width
	if inspector {
		right -= f.inspectorW
		f.inspector.Move(fyne.NewPos(right, 0))
		f.inspector.Resize(fyne.NewSize(f.inspectorW, size.Height))
		f.inspHandle.Move(fyne.NewPos(right-handleWidth, 0))
		f.inspHandle.Resize(fyne.NewSize(handleWidth, size.Height))
		right -= handleWidth
	}

	w := right - x
	if w < 0 {
		w = 0
	}
	f.main.Move(fyne.NewPos(x, 0))
	f.main.Resize(fyne.NewSize(w, size.Height))
}

func (f *chromeframe) CreateRenderer() fyne.WidgetRenderer {
	objects := []fyne.CanvasObject{f.sidebar, f.sideHandle, f.main, f.inspHandle, f.inspector}
	// Children visibility is driven by Layout; start with both panels hidden
	// so the first layout shows exactly what fits.
	f.sidebar.Hide()
	f.sideHandle.Hide()
	f.inspector.Hide()
	f.inspHandle.Hide()
	return &frameRenderer{frame: f, objects: objects}
}

type frameRenderer struct {
	frame   *chromeframe
	objects []fyne.CanvasObject
}

func (r *frameRenderer) Layout(size fyne.Size) { r.frame.Layout(size) }

func (r *frameRenderer) MinSize() fyne.Size {
	// The center column can shrink below a comfortable width on narrow
	// windows (panels auto-collapse); this minimum only guards the channel.
	return fyne.NewSize(channelMin()+2*handleWidth, 320)
}

func channelMin() float32 { return 300 }

func (r *frameRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *frameRenderer) Refresh() {}

func (r *frameRenderer) Destroy() {}

func toggleShown(o fyne.CanvasObject, show bool) {
	if o == nil {
		return
	}
	if show {
		o.Show()
	} else {
		o.Hide()
	}
}

// splitHandle is a thin draggable divider between panels.
type splitHandle struct {
	widget.BaseWidget
	hover  bool
	onDrag func(dx float32)
	onDone func()
}

func newSplitHandle(vertical bool, onDrag func(float32), onDone func()) *splitHandle {
	h := &splitHandle{onDrag: onDrag, onDone: onDone}
	h.ExtendBaseWidget(h)
	return h
}

func (h *splitHandle) Dragged(ev *fyne.DragEvent) {
	if h.onDrag != nil {
		h.onDrag(ev.Dragged.DX)
	}
}

func (h *splitHandle) DragEnd() {
	if h.onDone != nil {
		h.onDone()
	}
}

func (h *splitHandle) MouseIn(*desktop.MouseEvent) {
	h.hover = true
	h.Refresh()
}

func (h *splitHandle) MouseMoved(*desktop.MouseEvent) {}

func (h *splitHandle) MouseOut() {
	h.hover = false
	h.Refresh()
}

func (h *splitHandle) Cursor() desktop.Cursor { return desktop.HResizeCursor }

func (h *splitHandle) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(theme.Color(colorBorder))
	return &splitHandleRenderer{handle: h, bg: bg}
}

type splitHandleRenderer struct {
	handle *splitHandle
	bg     *canvas.Rectangle
}

func (r *splitHandleRenderer) Layout(size fyne.Size) {
	r.bg.Move(fyne.NewPos(0, 0))
	r.bg.Resize(size)
}

func (r *splitHandleRenderer) MinSize() fyne.Size {
	return fyne.NewSize(handleWidth, 0)
}

func (r *splitHandleRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.bg} }

func (r *splitHandleRenderer) Refresh() {
	if r.handle.hover {
		r.bg.FillColor = theme.Color(theme.ColorNamePrimary)
	} else {
		r.bg.FillColor = theme.Color(colorBorder)
	}
	r.bg.Refresh()
}

func (r *splitHandleRenderer) Destroy() {}
