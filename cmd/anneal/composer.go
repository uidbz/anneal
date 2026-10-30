package main

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// composerEntry is a multi-line composer where Enter sends and Shift+Enter
// inserts a newline (Fyne's default is the opposite). It also exposes
// Ctrl+Enter to the shell for "allow once" on a pending approval.
type composerEntry struct {
	widget.Entry
	shift    bool
	minRows  int
	onSend   func()
	onCtrlCR func()
}

func newComposerEntry() *composerEntry {
	e := &composerEntry{}
	e.ExtendBaseWidget(e)
	e.MultiLine = true
	e.Wrapping = fyne.TextWrapWord
	e.minRows = 2
	e.SetMinRowsVisible(2)
	e.OnChanged = func(string) { e.syncRows() }
	return e
}

// syncRows grows the entry from 2 to 8 rows as the text gains lines.
func (e *composerEntry) syncRows() {
	rows := 1 + strings.Count(e.Text, "\n")
	if rows < 2 {
		rows = 2
	}
	if rows > 8 {
		rows = 8
	}
	if rows != e.minRows {
		e.minRows = rows
		e.SetMinRowsVisible(rows)
	}
}

func (e *composerEntry) KeyDown(ev *fyne.KeyEvent) {
	if ev.Name == desktop.KeyShiftLeft || ev.Name == desktop.KeyShiftRight {
		e.shift = true
	}
	e.Entry.KeyDown(ev)
}

func (e *composerEntry) KeyUp(ev *fyne.KeyEvent) {
	if ev.Name == desktop.KeyShiftLeft || ev.Name == desktop.KeyShiftRight {
		e.shift = false
	}
	e.Entry.KeyUp(ev)
}

func (e *composerEntry) TypedKey(ev *fyne.KeyEvent) {
	switch ev.Name {
	case fyne.KeyReturn, fyne.KeyEnter:
		if e.shift {
			e.Entry.TypedRune('\n')
			return
		}
		if e.onSend != nil {
			e.onSend()
		}
		return
	}
	e.Entry.TypedKey(ev)
}

func (e *composerEntry) TypedShortcut(sc fyne.Shortcut) {
	if custom, ok := sc.(*desktop.CustomShortcut); ok {
		if custom.KeyName == fyne.KeyReturn && custom.Modifier&fyne.KeyModifierControl != 0 {
			if e.onCtrlCR != nil {
				e.onCtrlCR()
			}
			return
		}
	}
	e.Entry.TypedShortcut(sc)
}

// composer is the bottom input card: entry, mode switch, model, hint, and a
// single primary button that turns into Stop while a run is going.
type composer struct {
	s         *shell
	entry     *composerEntry
	object    fyne.CanvasObject
	card      *surface
	buildBtn  *widget.Button
	planBtn   *widget.Button
	modelBtn  *widget.Button
	hint      *themedText
	actionBtn *widget.Button
	running   bool
}

func newComposer(s *shell) *composer {
	c := &composer{s: s}

	c.entry = newComposerEntry()
	c.entry.PlaceHolder = "Describe a task…  (/ for commands)"
	c.entry.onSend = s.submit
	c.entry.onCtrlCR = s.allowOldest

	c.buildBtn = widget.NewButton("Build", func() { s.a.Runner.SetMode("default") })
	c.planBtn = widget.NewButton("Plan", func() { s.a.Runner.SetMode("plan") })
	c.buildBtn.Importance = widget.HighImportance
	c.planBtn.Importance = widget.LowImportance

	c.modelBtn = widget.NewButton("", func() { s.showSettings() })
	c.modelBtn.Importance = widget.LowImportance
	c.modelBtn.Hide()

	c.hint = newThemedText("Enter to send · Shift+Enter for a new line", colorMuted)
	c.hint.size = theme.SizeNameCaptionText

	c.actionBtn = widget.NewButtonWithIcon("Send", theme.MailSendIcon(), s.submit)
	c.actionBtn.Importance = widget.HighImportance

	modes := container.New(&rowLayout{gap: 0}, c.buildBtn, c.planBtn)
	right := container.New(&rowLayout{gap: spaceS}, c.modelBtn, c.actionBtn)
	toolbar := container.NewBorder(nil, nil, modes, right,
		container.New(&padLayout{l: spaceM, r: spaceM, t: 0, b: 0}, c.hint))

	body := container.NewVBox(c.entry, vgap(spaceXS), newDivider(), vgap(spaceXS), toolbar)
	c.card = newSurface(colorRaised, body).border(colorBorder, 1)
	c.card.padding(spaceM, spaceS, spaceM, spaceS)
	c.object = container.New(&padLayout{l: spaceL, r: spaceL, t: 0, b: spaceS}, c.card)
	c.setMode(s.mode)
	return c
}

func (c *composer) Object() fyne.CanvasObject { return c.object }

// setMode reflects the shell's permission mode in the switch.
func (c *composer) setMode(mode string) {
	if mode == "plan" {
		c.planBtn.Importance = widget.HighImportance
		c.buildBtn.Importance = widget.LowImportance
	} else {
		c.buildBtn.Importance = widget.HighImportance
		c.planBtn.Importance = widget.LowImportance
	}
	c.buildBtn.Refresh()
	c.planBtn.Refresh()
}

// setRunning swaps Send for Stop (and back).
func (c *composer) setRunning(running bool) {
	if c.running == running {
		return
	}
	c.running = running
	if running {
		c.actionBtn.SetText("Stop")
		c.actionBtn.SetIcon(theme.MediaStopIcon())
		c.actionBtn.Importance = widget.MediumImportance
		c.actionBtn.OnTapped = func() {
			c.s.status.SetText("Stopping…")
			c.s.a.Runner.Interrupt()
		}
	} else {
		c.actionBtn.SetText("Send")
		c.actionBtn.SetIcon(theme.MailSendIcon())
		c.actionBtn.Importance = widget.HighImportance
		c.actionBtn.OnTapped = c.s.submit
	}
	c.actionBtn.Refresh()
}

// setModel shows the model name in the toolbar (click opens Settings).
func (c *composer) setModel(model string, ctx int) {
	if model == "" {
		c.modelBtn.Hide()
		return
	}
	text := trunc(model, 26)
	if ctx > 0 {
		text += " · " + humanK(ctx)
	}
	c.modelBtn.SetText(text)
	c.modelBtn.Show()
}

// setCompact hides the shortcut hint when the window is narrow.
func (c *composer) setCompact(on bool) {
	if on {
		c.hint.Hide()
	} else {
		c.hint.Show()
	}
	c.hint.Refresh()
}

// focus puts the caret in the composer.
func (c *composer) focus() { c.s.win.Canvas().Focus(c.entry) }

// approvalDock is the stack of pending approvals above the composer. It
// scrolls once the cards would take more than ~40% of a typical window.
type approvalDock struct {
	widget.BaseWidget
	cards   *fyne.Container
	scroll  *container.Scroll
	visible bool
	surface *surface
}

func newApprovalDock() *approvalDock {
	d := &approvalDock{cards: container.NewVBox()}
	d.scroll = container.NewVScroll(d.cards)
	d.surface = newSurface(colorRaised, container.New(&padLayout{l: spaceL, r: spaceL, t: spaceS, b: 0}, d.scroll))
	d.surface.padding(0, 0, 0, 0)
	d.surface.Hide()
	d.ExtendBaseWidget(d)
	return d
}

func (d *approvalDock) CreateRenderer() fyne.WidgetRenderer {
	return &dockRenderer{dock: d}
}

type dockRenderer struct{ dock *approvalDock }

func (r *dockRenderer) Layout(size fyne.Size) {
	r.dock.surface.Move(fyne.NewPos(0, 0))
	r.dock.surface.Resize(size)
}

func (r *dockRenderer) MinSize() fyne.Size {
	if !r.dock.visible {
		return fyne.Size{}
	}
	h := r.dock.cards.MinSize().Height + 2*spaceS
	const max float32 = 320
	if h > max {
		h = max
	}
	return fyne.NewSize(0, h)
}

func (r *dockRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.dock.surface}
}

func (r *dockRenderer) Refresh() {}

func (r *dockRenderer) Destroy() {}

// add appends a card and shows the dock.
func (d *approvalDock) add(card fyne.CanvasObject) {
	d.cards.Add(card)
	d.cards.Add(vgap(spaceS))
	d.show(true)
}

// remove takes one card back out; the dock hides when nothing is pending.
func (d *approvalDock) remove(card fyne.CanvasObject) {
	d.cards.Remove(card)
	d.show(len(d.cards.Objects) > 1)
}

// clear empties the dock (new session / replay).
func (d *approvalDock) clear() {
	d.cards.RemoveAll()
	d.show(false)
}

func (d *approvalDock) show(on bool) {
	if d.visible == on {
		d.Refresh()
		return
	}
	d.visible = on
	if on {
		d.surface.Show()
	} else {
		d.surface.Hide()
	}
	d.Refresh()
}
