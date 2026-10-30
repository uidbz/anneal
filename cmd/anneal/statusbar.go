package main

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// chip is a clickable status-bar item: colored state dot + caption text.
type chip struct {
	widget.BaseWidget
	dot   *statusDot
	label *themedText
	onTap func()
	hover bool
	row   *fyne.Container
	bg    *canvas.Rectangle
}

func newChip(d *statusDot, text string, onTap func()) *chip {
	c := &chip{dot: d, label: newThemedText(text, colorMuted), onTap: onTap}
	c.label.size = theme.SizeNameCaptionText
	c.row = container.New(&rowLayout{gap: 5}, d, c.label)
	c.bg = canvas.NewRectangle(theme.Color(colorRail))
	c.ExtendBaseWidget(c)
	return c
}

func (c *chip) SetText(text string) {
	c.label.SetText(text)
	c.Refresh()
}

func (c *chip) Text() string { return c.label.Text() }

func (c *chip) Tapped(*fyne.PointEvent) {
	if c.onTap != nil {
		c.onTap()
	}
}

func (c *chip) MouseIn(*desktop.MouseEvent) {
	c.hover = true
	c.Refresh()
}

func (c *chip) MouseMoved(*desktop.MouseEvent) {}

func (c *chip) MouseOut() {
	c.hover = false
	c.Refresh()
}

func (c *chip) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (c *chip) CreateRenderer() fyne.WidgetRenderer {
	return &chipRenderer{chip: c}
}

type chipRenderer struct{ chip *chip }

func (r *chipRenderer) Layout(size fyne.Size) {
	r.chip.bg.Move(fyne.NewPos(0, 0))
	r.chip.bg.Resize(size)
	r.chip.row.Move(fyne.NewPos(spaceS, 0))
	r.chip.row.Resize(fyne.NewSize(size.Width-2*spaceS, size.Height))
}

func (r *chipRenderer) MinSize() fyne.Size {
	ms := r.chip.row.MinSize()
	return fyne.NewSize(ms.Width+2*spaceS, ms.Height+2)
}

func (r *chipRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.chip.bg, r.chip.row}
}

func (r *chipRenderer) Refresh() {
	if r.chip.hover {
		r.chip.bg.FillColor = theme.Color(theme.ColorNameHover)
	} else {
		r.chip.bg.FillColor = theme.Color(colorRail)
	}
	r.chip.bg.Refresh()
	r.chip.row.Refresh()
}

func (r *chipRenderer) Destroy() {}

// buildStatusBar creates the IDE-style bottom bar: run state on the left,
// clickable health chips and token totals on the right.
func (s *shell) buildStatusBar() fyne.CanvasObject {
	s.status = newThemedText("Ready", theme.ColorNameForeground)
	s.status.size = theme.SizeNameCaptionText
	s.elapsed = newThemedText("", colorMuted)
	s.elapsed.size = theme.SizeNameCaptionText

	s.runDot = newStatusDot()
	s.runDot.setToken(colorOK)

	left := container.New(&rowLayout{gap: spaceS}, s.runDot, s.status, s.elapsed)

	s.modelChip = newChip(newStatusDot(), "model: connecting…", s.showSettings)
	s.tieChip = newChip(newStatusDot(), "", s.confirmRebuildIndex)
	s.mcpChip = newChip(newStatusDot(), "", s.showSettings)
	s.tieChip.Hide()
	s.mcpChip.Hide()

	s.usage = newThemedText("", colorMuted)
	s.usage.size = theme.SizeNameCaptionText

	right := container.New(&rowLayout{gap: spaceS},
		s.modelChip, newVDividerSized(), s.tieChip, s.mcpChip, newVDividerSized(), s.usage)

	inner := container.NewBorder(nil, nil, left, right, nil)
	return newSurface(colorRail,
		container.New(&padLayout{l: spaceM, r: spaceL, t: 3, b: 3}, inner))
}

// newVDividerSized is a 1px vertical rule with a readable height.
func newVDividerSized() fyne.CanvasObject {
	d := newVDivider()
	d.Resize(fyne.NewSize(1, statusBarH-10))
	return container.New(&padLayout{l: spaceXS, r: spaceXS, t: 5, b: 5}, d)
}

// confirmRebuildIndex asks before re-indexing every session into tie.
func (s *shell) confirmRebuildIndex() {
	d := dialog.NewConfirm("Rebuild search index",
		"Re-index all sessions into the tie store? This can take a while for large histories.",
		func(ok bool) {
			if ok {
				s.slash("/index")
			}
		}, s.win)
	d.Show()
}

// runClock tracks elapsed time for the status bar while a run is active.
func (s *shell) startRunClock() {
	s.stopRunClock()
	s.runStarted = time.Now()
	stop := make(chan struct{})
	s.clockStop = stop
	s.elapsed.SetText("0s")
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				shown := time.Since(s.runStarted).Round(time.Second)
				fyne.Do(func() { s.elapsed.SetText(shown.String()) })
			}
		}
	}()
}

func (s *shell) stopRunClock() {
	if s.clockStop != nil {
		close(s.clockStop)
		s.clockStop = nil
	}
	s.elapsed.SetText("")
}

// ctxUsage renders the token totals/context label.
func (s *shell) setUsageText(tin, tout, pct int) {
	if tin == 0 && tout == 0 && pct == 0 {
		s.usage.SetText("")
		return
	}
	s.usage.SetText(fmt.Sprintf("↑ %s ↓ %s · ctx %d%%", humanK(tin), humanK(tout), pct))
}

// statusText is the current run-state label (tests and headers read it).
func (s *shell) statusText() string { return s.status.Text() }
