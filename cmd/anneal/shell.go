package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	coreapp "github.com/uidbz/anneal/core/app"
	"github.com/uidbz/anneal/core/config"
)

// textScaleStep is the A-/A+ (Ctrl+= / Ctrl+-) increment for ui.text_scale.
const textScaleStep = 0.1

// Transcript column: readable measure and block cap.
const (
	transcriptMaxWidth float32 = 820
	todoPanelWidth     float32 = 260
)

// viewID identifies the main content views.
type viewID int

const (
	viewChat viewID = iota
	viewHarness
	viewActivity
	viewSettings
)

func (v viewID) String() string {
	switch v {
	case viewHarness:
		return "Harness"
	case viewActivity:
		return "Activity"
	case viewSettings:
		return "Settings"
	default:
		return "Chat"
	}
}

// shell owns the Go/Fyne UI state. All widget mutation happens on the Fyne
// thread (via fyne.Do); the event consumer is a single ordered goroutine.
type shell struct {
	a   *coreapp.App
	app fyne.App
	win fyne.Window

	// Global status bar.
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

	// Navigation and frame.
	frame     *chromeframe
	rail      *rail
	navRail   map[viewID]*railItem
	views     map[viewID]fyne.CanvasObject
	view      viewID
	chatView  fyne.CanvasObject
	header    *header // chat header
	side      *sessionsSidebar
	inspector *inspectorPanel

	// Chat.
	cmp              *composer
	entry            *composerEntry
	dock             *approvalDock
	scroll           *container.Scroll
	chatBox          *fyne.Container // visible transcript blocks (append-only)
	earlier          *widget.Button  // "show earlier messages" when the window is capped
	visible          []fyne.CanvasObject
	hidden           []fyne.CanvasObject
	todos            *fyne.Container
	todoPane         fyne.CanvasObject
	activity         *fyne.Container
	activityHeader   *header
	settingsHeader   *header
	settings         fyne.CanvasObject
	settingsForm     *settingsPanel
	harnessHeader    *header
	harness          *scriptPane
	paneSrc          string
	permRows         map[string]fyne.CanvasObject
	pendingApprovals []pendingApproval

	composer  fyne.CanvasObject
	statusbar fyne.CanvasObject
	root      fyne.CanvasObject

	tail          *widget.Label
	tailBuf       string
	turn          blockKind
	toolRows      map[string]*toolRow
	pendingTools  []*toolRow
	progressLines map[string]*progressLine
	streamBlocks  []fyne.CanvasObject // frozen chunks of the live message
	welcome       fyne.CanvasObject
	mode          string
	improveAuto   bool // current improve pass was auto-triggered (notice wording)
	autoSend      string
	autoQuit      bool
	maxVisible    int

	title     string // current session title ("" = untitled)
	sessionID string
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
		navRail:    map[viewID]*railItem{},
		views:      map[viewID]fyne.CanvasObject{},
	}
	applyTheme(app, a.Cfg.UI)
	win.SetPadded(false)
	s.wireFrame()
	if err := s.buildHarness(paneSrc); err != nil {
		return nil, err
	}
	s.wireLayout()
	s.installMenu()
	return s, nil
}

func (s *shell) build() {
	s.status = widget.NewLabel("Ready")
	s.usage = widget.NewLabel("")
	s.modelLbl = widget.NewLabel("model: connecting…")
	s.tie = widget.NewLabel("")
	s.mcp = widget.NewLabel("")
	s.status.Wrapping = fyne.TextWrapOff // one short line; never wraps

	s.improveBtn = labeledButton("Improve", theme.MoveUpIcon(), s.improveNow)
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

	s.chatBox = container.NewVBox()
	s.earlier = widget.NewButton("", s.showEarlier)
	s.earlier.Importance = widget.LowImportance
	s.earlier.Hide()
	s.todos = container.NewVBox()
	s.activity = container.NewVBox()

	s.cmp = newComposer(s)
	s.entry = s.cmp.entry
	s.dock = newApprovalDock()

	// Status bar (bottom): run state + health chips + usage.
	s.statusbar = newSurface(colorRail, container.New(&padLayout{l: spaceM, r: spaceM, t: 3, b: 3},
		container.New(&rowLayout{gap: spaceM}, s.runDot, s.status, s.chipModel,
			s.chipTie, s.chipMCP, s.usage)))

}

// wireFrame builds the navigation chrome: rail, sidebar, chat view,
// inspector, and the secondary views.
func (s *shell) wireFrame() {
	s.build()

	s.header = newHeader("New session")
	s.header.setActions(
		iconButton(theme.MenuIcon(), s.toggleSidebar),
		s.improveBtn,
		iconButton(theme.ListIcon(), s.toggleInspector),
		iconButton(theme.MoreVerticalIcon(), s.showOverflow),
	)
	s.header.setBadge(modeBadge(s.mode))

	s.side = newSessionsSidebar(s)
	s.inspector = newInspectorPanel(s)

	// Transcript: centered, capped measure inside a scroll.
	padded := container.New(&padLayout{l: spaceL, r: spaceL, t: spaceS, b: spaceS},
		container.New(&maxWidthLayout{max: transcriptMaxWidth},
			container.NewVBox(s.earlier, s.chatBox)))
	s.scroll = container.NewVScroll(padded)
	s.welcome = s.welcomeView()
	transcript := container.NewStack(s.scroll, s.welcome)
	bottom := container.NewVBox(s.dock, s.cmp.Object())
	chatBody := container.NewBorder(nil, bottom, nil, nil, transcript)
	s.chatView = container.NewBorder(s.header.object, nil, nil, nil, chatBody)

	s.harnessHeader = newHeader("Harness")
	s.activityHeader = newHeader("Activity")
	s.activityHeader.setActions(labeledButton("Refresh", theme.ViewRefreshIcon(), s.refreshActivity))
	s.settingsHeader = newHeader("Settings")

	s.views[viewChat] = s.chatView
	s.views[viewActivity] = s.activityView()
	s.views[viewSettings] = s.settingsView()
}

// harnessView is the Harness page: Go header, scripted pane underneath.
func (s *shell) harnessView() fyne.CanvasObject {
	refresh := iconButton(theme.ViewRefreshIcon(), func() {
		s.a.Bus.Emit(map[string]any{"type": "harness.refresh"})
	})
	improve := labeledButton("Improve", theme.MoveUpIcon(), s.improveNow)
	s.harnessHeader.setActions(refresh, improve)
	return container.NewBorder(s.harnessHeader.object, nil, nil, nil, s.harness.view)
}

func (s *shell) activityView() fyne.CanvasObject {
	return container.NewBorder(s.activityHeader.object, nil, nil, nil,
		container.NewVScroll(newSurface(colorSidebar, container.New(&padLayout{l: spaceXL, r: spaceXL, t: spaceM, b: spaceM}, s.activity))))
}

func (s *shell) settingsView() fyne.CanvasObject {
	return container.NewBorder(s.settingsHeader.object, nil, nil, nil, s.settingsPage())
}

// showView switches the main content view and updates the rail.
func (s *shell) showView(v viewID) {
	if _, ok := s.views[v]; !ok {
		return
	}
	for id, view := range s.views {
		toggleShown(view, id == v)
	}
	for id, item := range s.navRail {
		item.SetActive(id == v)
	}
	s.view = v
	if s.frame != nil {
		s.frame.SetPanels(v == viewChat)
	}
}

func (s *shell) toggleSidebar() {
	open := !s.a.Cfg.UI.SidebarOpen
	s.a.Cfg.UI.SidebarOpen = open
	s.frame.SetSidebar(open)
	s.saveUIPrefs()
}

func (s *shell) toggleInspector() {
	open := !s.a.Cfg.UI.InspectorOpen
	s.a.Cfg.UI.InspectorOpen = open
	s.frame.SetInspector(open)
	s.saveUIPrefs()
}

// saveUIPrefs persists panel visibility/width without emitting a notice.
func (s *shell) saveUIPrefs() {
	if err := s.a.Cfg.Save(); err != nil {
		s.addNotice("⚠ settings save: " + err.Error())
	}
}

// showSessions focuses the chat view with the session list open.
func (s *shell) showSessions() {
	s.showView(viewChat)
	if !s.a.Cfg.UI.SidebarOpen {
		s.toggleSidebar()
	}
}

func (s *shell) showSettings() { s.showView(viewSettings) }

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
	s.addNotice("Harness improvement started")
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

// wireLayout assembles the root: rail | (sidebar | views | inspector), with
// the status bar across the bottom.
func (s *shell) wireLayout() {
	// The harness view was built before the pane existed; rebuild it now.
	s.views[viewHarness] = s.harnessView()
	s.showView(viewChat)

	s.frame = newChromeFrame(s.side.Object(), s.viewStack(),
		s.inspector.Object(), float32(s.a.Cfg.UI.SidebarWidth), float32(s.a.Cfg.UI.InspectorWidth))
	s.frame.onPanelResize = func(sidebar bool, width float32) {
		if sidebar {
			s.a.Cfg.UI.SidebarWidth = float64(width)
		} else {
			s.a.Cfg.UI.InspectorWidth = float64(width)
		}
		s.saveUIPrefs()
	}
	s.frame.SetSidebar(s.a.Cfg.UI.SidebarOpen)
	s.frame.SetInspector(s.a.Cfg.UI.InspectorOpen)

	s.root = container.NewBorder(nil, s.statusbar, s.railObject(), nil, s.frame)
	s.refreshActivity()
}

func (s *shell) viewStack() fyne.CanvasObject {
	stack := container.NewStack()
	for _, v := range []viewID{viewChat, viewHarness, viewActivity, viewSettings} {
		view := s.views[v]
		view.Hide()
		stack.Add(view)
	}
	s.showView(s.view)
	return stack
}

// railObject builds the activity rail once and remembers its items.
func (s *shell) railObject() fyne.CanvasObject {
	if s.rail != nil {
		return s.rail
	}
	item := func(v viewID, icon fyne.Resource) *railItem {
		it := newRailItem(icon, v.String(), func() { s.showView(v) })
		s.navRail[v] = it
		return it
	}
	chat := item(viewChat, theme.DocumentIcon())
	harness := item(viewHarness, theme.StorageIcon())
	activity := item(viewActivity, theme.HistoryIcon())
	settings := item(viewSettings, theme.SettingsIcon())
	logo := logoResource()
	s.rail = newRail(
		[]fyne.CanvasObject{chat, harness, activity},
		settings, logo)
	s.navRail[viewChat] = chat
	return s.rail
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
	fyne.Do(func() { s.win.Canvas().Focus(s.entry) })
	if s.autoSend != "" {
		time.AfterFunc(700*time.Millisecond, func() {
			fyne.Do(func() {
				s.entry.SetText(s.autoSend)
				s.submit()
			})
		})
	}
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

// setThemeMode applies and persists ui.theme (system/light/dark).
func (s *shell) setThemeMode(mode string) {
	s.a.Cfg.UI.Theme = normMode(mode)
	applyTheme(s.app, s.a.Cfg.UI)
	if err := s.a.Cfg.Save(); err != nil {
		s.addNotice("⚠ theme save: " + err.Error())
	}
	s.addNotice("Theme: " + s.a.Cfg.UI.ThemeMode())
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

// modeBadge returns the header pill text/colors for a permission mode.
func modeBadge(mode string) (string, fyne.ThemeColorName, fyne.ThemeColorName) {
	if mode == "plan" {
		return "PLAN", colorWarnWash, colorWarn
	}
	return "BUILD", colorRaised, colorMuted
}

// clampScale is a local alias for config clamping (tests use it directly).
func clampScale(v float64) float64 { return config.ClampTextScale(v) }

// setWindowTitle reflects the session title in the OS window title.
func (s *shell) setWindowTitle() {
	if s.title == "" {
		s.win.SetTitle("Anneal")
		s.header.setTitle("New session")
		return
	}
	s.win.SetTitle("Anneal — " + s.title)
	s.header.setTitle(trunc(s.title, 80))
}

// refreshSessionMeta re-reads the current session's title (the runner sets it
// after the first run) and refreshes the sidebar.
func (s *shell) refreshSessionMeta() {
	for _, info := range s.a.Runner.Sessions() {
		if info.ID == s.sessionID {
			s.title = info.Title
			s.setWindowTitle()
			break
		}
	}
	s.side.refresh()
}

// improveBusy toggles the Improve action's busy state so a second pass cannot
// be triggered (the core guards too, this is the visible affordance).
func (s *shell) improveBusy(busy bool) {
	if s.improveBtn == nil {
		return
	}
	if busy {
		s.improveBtn.SetText("Improving…")
		s.improveBtn.Disable()
		return
	}
	s.improveBtn.SetText("Improve")
	s.improveBtn.Enable()
}
