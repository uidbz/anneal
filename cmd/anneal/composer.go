package main

import (
	"fmt"
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
	onCtrlCR func() // Ctrl+Enter: allow once
	onCtrlSh func() // Ctrl+Shift+Enter: always allow
	onEscape func() // Esc: reject the oldest pending approval
	onTyping func() // first keystroke after empty: yield background GPU work
	hadText  bool
}

func newComposerEntry() *composerEntry {
	e := &composerEntry{}
	e.ExtendBaseWidget(e)
	e.MultiLine = true
	e.Wrapping = fyne.TextWrapWord
	e.minRows = 2
	e.SetMinRowsVisible(2)
	e.OnChanged = func(text string) {
		e.syncRows()
		if text != "" && !e.hadText && e.onTyping != nil {
			e.onTyping()
		}
		e.hadText = text != ""
	}
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
	case fyne.KeyEscape:
		if e.onEscape != nil && e.Text == "" {
			e.onEscape()
			return
		}
	}
	e.Entry.TypedKey(ev)
}

func (e *composerEntry) TypedShortcut(sc fyne.Shortcut) {
	if custom, ok := sc.(*desktop.CustomShortcut); ok {
		if custom.KeyName == fyne.KeyReturn && custom.Modifier&fyne.KeyModifierControl != 0 {
			if custom.Modifier&fyne.KeyModifierShift != 0 {
				if e.onCtrlSh != nil {
					e.onCtrlSh()
				}
			} else if e.onCtrlCR != nil {
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
	c.entry.onCtrlSh = s.alwaysAllowOldest
	c.entry.onEscape = s.rejectOldest
	c.entry.onTyping = s.b.preempt

	c.buildBtn = widget.NewButton("Build", func() { s.b.setMode("default") })
	c.planBtn = widget.NewButton("Plan", func() { s.b.setMode("plan") })
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
			c.s.b.interrupt()
		}
		c.hint.SetText("Queue a follow-up… Enter sends it after this task · Ctrl+D stops")
	} else {
		c.actionBtn.SetText("Send")
		c.actionBtn.SetIcon(theme.MailSendIcon())
		c.actionBtn.Importance = widget.HighImportance
		c.actionBtn.OnTapped = c.s.submit
		c.hint.SetText("Enter to send · Shift+Enter for a new line")
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

// approvalDock shows at most one pending approval at a time, oldest first,
// above the composer. Showing one full card keeps its buttons on screen even
// when dozens of requests queue up; the header counts the rest and pages
// through them. The dock used to append a spacer with every card and only
// remove the card, so spacers accumulated: the empty box stayed visible and
// new cards were pushed below the height cap.
type approvalDock struct {
	widget.BaseWidget
	s *shell

	cards   []fyne.CanvasObject // pending cards, oldest first
	idx     int
	visible bool

	header  *fyne.Container
	counter *themedText
	prev    *widget.Button
	next    *widget.Button
	body    *fyne.Container
	scroll  *container.Scroll
	inner   *fyne.Container
	surface *surface
}

// approvalDockFrac caps the dock at this fraction of the window height.
const approvalDockFrac = 0.45

func newApprovalDock(s *shell) *approvalDock {
	d := &approvalDock{s: s}
	d.counter = newThemedText("", colorWarn)
	d.counter.size = theme.SizeNameCaptionText
	d.counter.bold = true
	d.prev = iconButton(theme.NavigateBackIcon(), func() { d.selectIdx(d.idx - 1) })
	d.next = iconButton(theme.NavigateNextIcon(), func() { d.selectIdx(d.idx + 1) })
	d.header = container.NewBorder(nil, nil,
		container.New(&padLayout{l: spaceS, r: 0, t: 2, b: 2}, d.counter),
		container.New(&rowLayout{gap: 0}, d.prev, d.next), nil)
	d.body = container.New(&columnLayout{gap: 0})
	d.scroll = container.NewVScroll(d.body)
	d.inner = container.NewBorder(d.header, nil, nil, nil, d.scroll)
	d.surface = newSurface(colorRaised, d.inner)
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
	if !r.dock.visible || len(r.dock.cards) == 0 {
		return fyne.Size{}
	}
	h := r.dock.body.MinSize().Height + 2*spaceS
	if len(r.dock.cards) > 1 {
		h += r.dock.header.MinSize().Height
	}
	if max := r.dock.maxHeight(); max > 0 && h > max {
		h = max
	}
	if h < 80 {
		h = 80
	}
	return fyne.NewSize(0, h)
}

func (r *dockRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.dock.surface}
}

func (r *dockRenderer) Refresh() { r.dock.surface.Refresh() }

func (r *dockRenderer) Destroy() {}

// maxHeight is a share of the window, so the dock can never crowd out the
// composer and transcript on short or large-text windows.
func (d *approvalDock) maxHeight() float32 {
	if d.s == nil || d.s.win == nil {
		return 320
	}
	h := d.s.win.Canvas().Size().Height * approvalDockFrac
	if h < 160 {
		h = 160
	}
	return h
}

// add appends a card and shows it when it is the oldest pending one.
func (d *approvalDock) add(card fyne.CanvasObject) {
	d.cards = append(d.cards, card)
	if len(d.cards) == 1 {
		d.idx = 0
	}
	d.render()
}

// remove takes one card back out; the dock hides when nothing is pending.
func (d *approvalDock) remove(card fyne.CanvasObject) {
	for i, c := range d.cards {
		if c == card {
			d.cards = append(d.cards[:i], d.cards[i+1:]...)
			break
		}
	}
	if d.idx >= len(d.cards) {
		d.idx = len(d.cards) - 1
	}
	if d.idx < 0 {
		d.idx = 0
	}
	d.render()
}

// clear empties the dock (new session / replay).
func (d *approvalDock) clear() {
	d.cards = nil
	d.idx = 0
	d.render()
}

// selectIdx pages to another pending card (the oldest stays the default so
// Ctrl+Enter always answers what the agent is blocked on).
func (d *approvalDock) selectIdx(i int) {
	if i < 0 || i >= len(d.cards) || i == d.idx {
		return
	}
	d.idx = i
	d.render()
}

// active reports the id of the card on screen (for tests/diagnostics).
func (d *approvalDock) active() fyne.CanvasObject {
	if d.idx < 0 || d.idx >= len(d.cards) {
		return nil
	}
	return d.cards[d.idx]
}

func (d *approvalDock) render() {
	on := len(d.cards) > 0
	d.visible = on
	d.body.RemoveAll()
	if on {
		d.body.Add(d.cards[d.idx])
		d.surface.Show()
	} else {
		d.surface.Hide()
	}
	if len(d.cards) > 1 {
		d.counter.SetText(fmt.Sprintf("%d approvals waiting · %d of %d",
			len(d.cards), d.idx+1, len(d.cards)))
		d.header.Show()
		d.prev.Enable()
		d.next.Enable()
		if d.idx <= 0 {
			d.prev.Disable()
		}
		if d.idx >= len(d.cards)-1 {
			d.next.Disable()
		}
	} else {
		d.header.Hide()
	}
	d.scroll.ScrollToTop()
	d.Refresh()
	if d.s != nil {
		d.s.relayoutChat()
	}
}
