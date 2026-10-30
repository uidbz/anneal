package main

import (
	"context"
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	coreapp "github.com/uidbz/anneal/core/app"
)

// textScaleStep is the A-/A+ (Ctrl+= / Ctrl+-) increment for ui.text_scale.
const textScaleStep = 0.1

// todoPanelWidth keeps the todo sidebar readable instead of collapsing to the
// minimum width of its wrapped labels inside a Border east slot.
const todoPanelWidth = 260

// shell owns the Go/Fyne UI state. All widget mutation happens on the Fyne
// thread (via fyne.Do); the event consumer is a single ordered goroutine.
type shell struct {
	a   *coreapp.App
	app fyne.App
	win fyne.Window

	status     *widget.Label // run state: ready/running…/improving…
	runDot     *statusDot
	usage      *widget.Label
	modelLbl   *widget.Label // model server chip
	modelDot   *statusDot
	chipModel  *fyne.Container
	tie        *widget.Label
	tieDot     *statusDot
	chipTie    *fyne.Container
	mcp        *widget.Label
	mcpDot     *statusDot
	chipMCP    *fyne.Container
	improveBtn *widget.Button
	modeBtn    *widget.Button
	entry      *widget.Entry
	scroll     *container.Scroll
	chatBox    *fyne.Container // visible transcript blocks (append-only)
	earlier    *widget.Button  // "show earlier messages" when the window is capped
	visible    []fyne.CanvasObject
	hidden     []fyne.CanvasObject
	perms      *fyne.Container
	todos      *fyne.Container
	todoPane   fyne.CanvasObject
	activity   *fyne.Container
	tabs       *container.AppTabs
	harness    *scriptPane
	paneSrc    string
	permRows   map[string]fyne.CanvasObject

	header    fyne.CanvasObject
	composer  fyne.CanvasObject
	statusbar fyne.CanvasObject
	root      fyne.CanvasObject

	tail         *widget.Label
	tailBuf      string
	streamBlocks []fyne.CanvasObject // frozen chunks of the live message
	mode         string
	improveAuto  bool // current improve pass was auto-triggered (notice wording)
	autoSend     string
	autoQuit     bool
	maxVisible   int
}

func newShell(a *coreapp.App, app fyne.App, win fyne.Window, paneSrc string) (*shell, error) {
	s := &shell{
		a:          a,
		app:        app,
		win:        win,
		permRows:   map[string]fyne.CanvasObject{},
		mode:       a.Runner.Mode(),
		paneSrc:    paneSrc,
		maxVisible: defaultVisibleBlocks,
	}
	applyTheme(app, a.Cfg.UI)
	s.build()
	if err := s.buildHarness(paneSrc); err != nil {
		return nil, err
	}
	s.wireLayout()
	return s, nil
}

func (s *shell) build() {
	s.status = widget.NewLabel("ready")
	s.usage = widget.NewLabel("")
	s.modelLbl = widget.NewLabel("model: connecting…")
	s.tie = widget.NewLabel("")
	s.mcp = widget.NewLabel("")
	s.status.Wrapping = fyne.TextWrapOff // one short line; never wraps

	brand := widget.NewLabelWithStyle("◆ anneal", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	s.modeBtn = widget.NewButton(modeTitle(s.mode), func() {
		if s.mode == "plan" {
			s.a.Runner.SetMode("default")
		} else {
			s.a.Runner.SetMode("plan")
		}
	})
	newBtn := widget.NewButton("+ new", func() { s.a.Runner.NewSession() })
	sessBtn := widget.NewButton("sessions", s.showSessions)
	settingsBtn := widget.NewButton("settings", s.showSettings)
	fontDownBtn := widget.NewButton("A-", func() { s.nudgeTextScale(-textScaleStep) })
	fontUpBtn := widget.NewButton("A+", func() { s.nudgeTextScale(textScaleStep) })

	s.improveBtn = widget.NewButtonWithIcon("improve", theme.MoveUpIcon(), s.improveNow)
	if !s.a.Cfg.Improve.Enabled {
		s.improveBtn.Hide()
	}

	s.runDot = newStatusDot()
	s.runDot.setToken(colorOK)
	s.modelDot = newStatusDot()
	s.tieDot = newStatusDot()
	s.mcpDot = newStatusDot()
	s.chipModel = newChip(s.modelDot, s.modelLbl)
	s.chipTie = newChip(s.tieDot, s.tie)
	s.chipMCP = newChip(s.mcpDot, s.mcp)
	s.chipTie.Hide()
	s.chipMCP.Hide()

	s.entry = widget.NewEntry()
	s.entry.PlaceHolder = "describe a task — Enter to send"
	s.entry.OnSubmitted = func(string) { s.submit() }

	s.chatBox = container.NewVBox()
	s.earlier = widget.NewButton("", s.showEarlier)
	s.earlier.Hide()
	s.perms = container.NewVBox()
	s.todos = container.NewVBox()
	s.activity = container.NewVBox()

	sendBtn := widget.NewButtonWithIcon("send", theme.MailSendIcon(), s.submit)
	stopBtn := widget.NewButtonWithIcon("stop", theme.MediaStopIcon(), func() {
		s.status.SetText("stopping…")
		s.a.Runner.Interrupt()
	})

	left := container.New(&rowLayout{gap: 8}, brand, s.modeBtn, newBtn, sessBtn,
		settingsBtn, s.improveBtn, fontDownBtn, fontUpBtn)
	rule := newDivider()
	s.header = container.New(&padLayout{l: 12, r: 12, t: 8, b: 6},
		container.NewBorder(nil, rule, left, nil, nil))
	s.composer = container.New(&padLayout{l: 12, r: 12, t: 6, b: 2},
		container.NewBorder(nil, nil, nil,
			container.NewHBox(sendBtn, hgap(6), stopBtn), s.entry))
	// Status bar (bottom, IDE-style): run state + health chips + usage.
	// Keeping it out of the header keeps the header's minimum window
	// width small enough for narrow windows.
	s.statusbar = container.New(&padLayout{l: 12, r: 12, t: 2, b: 8},
		container.New(&rowLayout{gap: 14}, s.runDot, s.status, s.chipModel,
			s.chipTie, s.chipMCP, s.usage))
}

// trunc keeps header chips from forcing a huge window minimum width.
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// humanK formats token counts compactly (262144 → 262k).
func humanK(n int) string {
	if n >= 1000 {
		return strconv.Itoa(n/1000) + "k"
	}
	return strconv.Itoa(n)
}

// newChip packs a status dot with a caption-emphasis label.
func newChip(d *statusDot, l *widget.Label) *fyne.Container {
	l.Importance = widget.LowImportance
	return container.New(&rowLayout{gap: 6}, d, l)
}

// setRunState updates the run-state label and its dot together.
func (s *shell) setRunState(text string, dot fyne.ThemeColorName, alarm bool) {
	s.status.SetText(text)
	s.runDot.setToken(dot)
	if alarm {
		s.status.Importance = widget.HighImportance
	} else {
		s.status.Importance = widget.MediumImportance
	}
}

// improveNow runs one harness-improvement pass (header button, Ctrl+I, and
// /improve). Guards live in ImproveWith; failures arrive as improve.error.
func (s *shell) improveNow() {
	s.addNotice("🔧 harness improvement started")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
		defer cancel()
		if _, err := s.a.Improve(ctx); err != nil {
			s.a.Bus.Emit(map[string]any{"type": "improve.error", "error": err.Error()})
		}
	}()
}

func (s *shell) buildHarness(src string) error {
	p, err := newScriptPane(s.win, src, harnessPaneCaps, s.a.Bus, s.a.HarnessAPI(), paneEvent)
	if err != nil {
		return fmt.Errorf("harness pane: %w", err)
	}
	s.harness = p
	return nil
}

func (s *shell) wireLayout() {
	// Breathing room: the transcript is inset from the window edges.
	padded := container.New(&padLayout{l: 12, r: 12, t: 4, b: 2},
		container.NewVBox(s.earlier, s.chatBox))
	s.scroll = container.NewVScroll(padded)
	// The transparent spacer only exists to give the Border east slot a
	// readable minimum width; the todo pane itself is hidden while empty.
	spacer := canvas.NewRectangle(color.Transparent)
	spacer.SetMinSize(fyne.NewSize(todoPanelWidth, 0))
	todoBody := container.NewVBox(
		widget.NewLabelWithStyle("todos", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		s.todos,
	)
	// Faint card behind the todo list so the sidebar reads as one panel.
	s.todoPane = container.NewStack(spacer,
		newSurface(colorRaised, container.New(&padLayout{l: 10, r: 10, t: 8, b: 8}, todoBody)))
	s.todoPane.Hide()
	chat := container.NewBorder(
		container.NewVBox(s.perms),
		nil, nil,
		s.todoPane,
		s.scroll,
	)
	s.tabs = container.NewAppTabs(
		container.NewTabItem("Chat", chat),
		container.NewTabItem("Harness", s.harness.view),
		container.NewTabItem("Activity", container.NewVScroll(s.activity)),
	)
	s.root = container.NewBorder(s.header,
		container.NewVBox(s.composer, s.statusbar), nil, nil, s.tabs)
	s.refreshActivity()
}

func (s *shell) start(ctx context.Context) {
	events := make(chan any, 1024)
	s.a.Bus.AddTap(func(v any) { events <- v })
	go func() {
		for v := range events {
			ev := normalizeEvent(v)
			fyne.Do(func() { s.handleEvent(ev) })
		}
	}()

	// app.New resumed the session before the Go tap existed.
	s.a.ReplayState()

	s.harness.Execute()
	s.a.Start(ctx)
	s.installShortcuts()
	fyne.Do(func() { s.win.Canvas().Focus(s.entry) })
	if s.autoSend != "" {
		time.AfterFunc(700*time.Millisecond, func() {
			fyne.Do(func() {
				s.entry.SetText(s.autoSend)
				s.submit()
			})
		})
	}
	time.AfterFunc(500*time.Millisecond, func() {
		// GLFW/Wayland ignores pre-show resize hints on some compositors.
		fyne.Do(func() { s.win.Resize(fyne.NewSize(1100, 750)) })
	})
}

func (s *shell) submit() {
	text := strings.TrimSpace(s.entry.Text)
	if text == "" {
		return
	}
	s.entry.SetText("")
	if strings.HasPrefix(text, "/") {
		s.slash(text)
		return
	}
	s.addUser(text)
	s.a.Runner.Send(text)
	s.win.Canvas().Focus(s.entry)
	s.scroll.ScrollToBottom()
}

// installShortcuts wires the navigation keys plus pane reload/quit.
func (s *shell) installShortcuts() {
	add := func(key fyne.KeyName, mod fyne.KeyModifier, fn func()) {
		s.win.Canvas().AddShortcut(&desktop.CustomShortcut{KeyName: key, Modifier: mod},
			func(fyne.Shortcut) { fn() })
	}
	add(fyne.KeyN, fyne.KeyModifierControl, func() { s.a.Runner.NewSession() })
	add(fyne.KeyB, fyne.KeyModifierControl, s.showSessions)
	add(fyne.KeyD, fyne.KeyModifierControl, func() { s.a.Runner.Interrupt() })
	add(fyne.KeyI, fyne.KeyModifierControl, s.improveNow)
	add(fyne.KeyL, fyne.KeyModifierControl, func() { s.win.Canvas().Focus(s.entry) })
	add(fyne.KeyR, fyne.KeyModifierControl, s.reloadPane)
	add(fyne.KeyQ, fyne.KeyModifierControl, func() { s.app.Quit() })
	add(fyne.KeyEqual, fyne.KeyModifierControl, func() { s.nudgeTextScale(textScaleStep) })
	add(fyne.KeyPlus, fyne.KeyModifierControl, func() { s.nudgeTextScale(textScaleStep) })
	add(fyne.KeyMinus, fyne.KeyModifierControl, func() { s.nudgeTextScale(-textScaleStep) })
	add(fyne.Key0, fyne.KeyModifierControl, func() { s.setTextScale(1.0) })
	add(fyne.Key1, fyne.KeyModifierControl, func() { s.tabs.SelectIndex(0) })
	add(fyne.Key2, fyne.KeyModifierControl, func() { s.tabs.SelectIndex(1) })
	add(fyne.Key3, fyne.KeyModifierControl, func() { s.tabs.SelectIndex(2) })
}

// setTextScale applies and persists a text-size multiplier. SetTheme repaints
// every window (Fyne re-layouts all renderers), so the change is live; the
// config save makes it survive restart. The config.changed event appends the
// user-facing notice.
func (s *shell) setTextScale(scale float64) {
	scale = s.a.SetTextScale(scale) // clamp + persist + config.changed
	applyTheme(s.app, s.a.Cfg.UI.WithTextScale(scale))
}

// nudgeTextScale steps the text size by delta (A-/A+ buttons, Ctrl+= / Ctrl+-).
func (s *shell) nudgeTextScale(delta float64) {
	s.setTextScale(s.a.Cfg.UI.TextScale + delta)
}

// reloadPane re-reads the pane script from disk (or the embedded copy),
// recompiles it, and restarts the embedded runtime without touching the shell.
func (s *shell) reloadPane() {
	src, err := paneSource(s.a.Cfg.UI.PaneDir)
	if err != nil {
		s.addNotice("⚠ pane reload: " + err.Error())
		return
	}
	if err := s.harness.LoadScript(src); err != nil {
		s.addNotice("⚠ pane reload: " + err.Error())
		return
	}
	s.paneSrc = src
	s.harness.Execute()
	s.addNotice("✔ harness pane reloaded")
}
