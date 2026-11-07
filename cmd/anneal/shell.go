package main

import (
	"context"
	"encoding/json"
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
	"github.com/uidbz/anneal/core/events"
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
// The core is reached through b (local App or remote anneald); a stays for
// client-side concerns (local config/UI prefs) and is nil-safe in remote mode.
type shell struct {
	a   *coreapp.App
	b   backend
	cfg *config.Config // client config: UI prefs + backend registry (always local)
	bus *events.Bus    // shell-side event stream (backend-forwarded + local notices)
	app fyne.App
	win fyne.Window

	// Global status bar.
	status      *themedText // run state: Ready/Running…/Improving…
	runDot      *statusDot
	usage       *themedText
	elapsed     *themedText
	modelChip   *chip
	projectChip *chip
	tieChip     *chip
	mcpChip     *chip
	backendChip *chip
	improveBtn  *widget.Button
	clockStop   chan struct{}
	runStarted  time.Time
	ctxWindow   int
	mcpServers  []map[string]any
	tieShown    bool
	mcpShown    bool

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
	queueTray        *queueTray
	chatBody         *fyne.Container
	queued           []queuedItem
	queuePaused      bool
	queueReason      string
	userFooters      map[string]*themedText
	runRefs          map[int]string
	refSeq           int
	gortexNotice     bool
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
	lastStats     map[string]any // last run.stats payload (/perf, inspector)

	title     string // current session title ("" = untitled)
	sessionID string
}

func newShell(a *coreapp.App, b backend, cfg *config.Config, app fyne.App, win fyne.Window, paneSrc string) (*shell, error) {
	s := &shell{
		a:          a,
		b:          b,
		cfg:        cfg,
		bus:        events.New(),
		app:        app,
		win:        win,
		permRows:   map[string]fyne.CanvasObject{},
		mode:       b.mode(),
		paneSrc:    paneSrc,
		maxVisible: defaultVisibleBlocks,
		navRail:    map[viewID]*railItem{},
		views:      map[viewID]fyne.CanvasObject{},
	}
	b.attach(s.bus)
	applyTheme(app, cfg.UI)
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
	s.improveBtn = labeledButton("Improve", theme.MoveUpIcon(), s.improveNow)
	if !s.b.improveEnabled() {
		s.improveBtn.Hide()
	}

	s.chatBox = container.New(&columnLayout{gap: spaceS})
	s.earlier = widget.NewButton("", s.showEarlier)
	s.earlier.Importance = widget.LowImportance
	s.earlier.Hide()
	s.todos = container.New(&columnLayout{gap: spaceXS})
	s.activity = container.New(&columnLayout{gap: spaceS})

	s.cmp = newComposer(s)
	s.entry = s.cmp.entry
	s.dock = newApprovalDock(s)
	s.queueTray = newQueueTray(s)
	s.userFooters = map[string]*themedText{}
	s.runRefs = map[int]string{}

	s.statusbar = s.buildStatusBar()

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

	// Transcript: left-aligned against the sidebar, capped measure inside a
	// scroll (centering it left a large dead margin on wide windows).
	padded := container.New(&padLayout{l: spaceM, r: spaceL, t: spaceS, b: spaceS},
		container.New(&maxWidthLayout{max: transcriptMaxWidth, left: true},
			container.NewVBox(s.earlier, s.chatBox)))
	s.scroll = container.NewVScroll(padded)
	s.welcome = s.welcomeView()
	transcript := container.NewStack(s.scroll, s.welcome)
	bottom := container.NewVBox(s.dock, s.queueTray.Object(), s.cmp.Object())
	s.chatBody = container.NewBorder(nil, bottom, nil, nil, transcript)
	s.chatView = container.NewBorder(s.header.object, nil, nil, nil, s.chatBody)

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
		s.bus.Emit(map[string]any{"type": "harness.refresh"})
	})
	improve := labeledButton("Improve", theme.MoveUpIcon(), s.improveNow)
	s.harnessHeader.setActions(refresh, improve)
	return container.NewBorder(s.harnessHeader.object, nil, nil, nil, s.harness.view)
}

func (s *shell) activityView() fyne.CanvasObject {
	body := newSurface(colorSidebar, container.New(&padLayout{l: spaceXL, r: spaceXL, t: spaceM, b: spaceM}, s.activity))
	return container.NewBorder(s.activityHeader.object, nil, nil, nil,
		container.NewVScroll(container.New(&padLayout{l: 0, r: 0, t: 0, b: 0},
			container.New(&maxWidthLayout{max: 900}, body))))
}

func (s *shell) settingsView() fyne.CanvasObject {
	s.settingsForm = newSettingsPanel(s)
	return container.NewBorder(s.settingsHeader.object, nil, nil, nil, s.settingsForm.Object())
}

// showView switches the main content view and updates the rail.
func (s *shell) showView(v viewID) {
	if _, ok := s.views[v]; !ok {
		return
	}
	// Leaving Settings with unsaved edits asks first.
	if s.view == viewSettings && v != viewSettings && s.settingsForm != nil && s.settingsForm.dirty {
		s.settingsForm.confirmDiscard(func() { s.showView(v) })
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
	open := !s.cfg.UI.SidebarOpen
	s.cfg.UI.SidebarOpen = open
	s.frame.SetSidebar(open)
	s.saveUIPrefs()
}

func (s *shell) toggleInspector() {
	open := !s.cfg.UI.InspectorOpen
	s.cfg.UI.InspectorOpen = open
	s.frame.SetInspector(open)
	s.saveUIPrefs()
}

// configDraft decodes the backend's live config document into a Config for
// the settings form (works for local and remote backends).
func (s *shell) configDraft() (config.Config, error) {
	doc, err := s.b.configDoc()
	if err != nil {
		return *config.Default(), err
	}
	var cfg config.Config
	if err := json.Unmarshal([]byte(doc), &cfg); err != nil {
		return *config.Default(), err
	}
	return cfg, nil
}

// saveUIPrefs persists panel visibility/width without emitting a notice.
func (s *shell) saveUIPrefs() {
	if err := s.cfg.Save(); err != nil {
		s.addNotice("⚠ settings save: " + err.Error())
	}
}

// showSessions focuses the chat view with the session list open.
func (s *shell) showSessions() {
	s.showView(viewChat)
	if !s.cfg.UI.SidebarOpen {
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

// setRunState updates the run-state label and its dot together.
func (s *shell) setRunState(text string, dot fyne.ThemeColorName, alarm bool) {
	s.status.SetText(text)
	s.runDot.setToken(dot)
	if alarm {
		s.status.token = colorErr
	} else {
		s.status.token = theme.ColorNameForeground
	}
	s.status.Refresh()
}

// improveNow runs one harness-improvement pass (header button, Ctrl+I, and
// /improve). The backend guards double starts; failures arrive as improve.error.
func (s *shell) improveNow() {
	s.addNotice("Harness improvement started")
	s.b.improveStart()
}

func (s *shell) buildHarness(src string) error {
	p, err := newScriptPane(s.win, src, harnessPaneCaps, s.bus, s.b.paneAPI(), paneEvent)
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
		s.inspector.Object(), float32(s.cfg.UI.SidebarWidth), float32(s.cfg.UI.InspectorWidth))
	s.frame.onPanelResize = func(sidebar bool, width float32) {
		if sidebar {
			s.cfg.UI.SidebarWidth = float64(width)
		} else {
			s.cfg.UI.InspectorWidth = float64(width)
		}
		s.saveUIPrefs()
	}
	s.frame.onWidth = func(w float32) { s.updateCompact() }
	s.frame.SetSidebar(s.cfg.UI.SidebarOpen)
	s.frame.SetInspector(s.cfg.UI.InspectorOpen)

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
	s.bus.AddTap(func(v any) { events <- v })
	// One consumer, with coalescing for the high-frequency stream events:
	// Fyne's main queue is unbounded, so a burst of per-token updates makes
	// the UI lag behind (and delays the Stop button) instead of dropping.
	go func() {
		ticker := time.NewTicker(40 * time.Millisecond)
		defer ticker.Stop()
		var pending map[string]any
		flush := func() {
			if pending == nil {
				return
			}
			ev := pending
			pending = nil
			fyne.Do(func() { s.handleEvent(ev) })
		}
		for {
			select {
			case v, ok := <-events:
				if !ok {
					flush()
					return
				}
				ev := normalizeEvent(v)
				typ := str(ev["type"])
				if typ == "text.delta" || typ == "thinking.delta" || typ == "tool.composing" {
					if pending != nil && str(pending["type"]) == typ &&
						str(pending["run"]) == str(ev["run"]) && str(pending["id"]) == str(ev["id"]) {
						if typ == "tool.composing" {
							pending["bytes"] = num(pending["bytes"]) + num(ev["bytes"])
							if name := str(ev["name"]); name != "" {
								pending["name"] = name
							}
						} else {
							pending["text"] = str(pending["text"]) + str(ev["text"])
						}
						continue
					}
					flush()
					pending = ev
					continue
				}
				flush()
				fyne.Do(func() { s.handleEvent(ev) })
			case <-ticker.C:
				flush()
			}
		}
	}()

	s.harness.Execute()
	// The backend replays the resumed session once the tap exists (no-op
	// for remote: the server replays on connect).
	s.b.start(ctx)
	// The home-directory warning must come after the resumed transcript (a
	// session.opened replay resets the chat), and only for a local core.
	time.AfterFunc(300*time.Millisecond, func() {
		fyne.Do(func() { s.showHomeDirNotice() })
	})
	fyne.Do(func() { s.win.Canvas().Focus(s.entry) })
	// Some Wayland compositors ignore pre-show resize hints and open at a
	// smaller size, which would auto-collapse the panels. Re-apply the
	// intended size and lay the frame out once the window is mapped.
	time.AfterFunc(500*time.Millisecond, func() {
		fyne.Do(func() {
			// Only correct an undersized first map; a deliberate user size
			// (>= the inspector threshold) is left alone.
			if sz := s.win.Canvas().Size(); sz.Width < narrowInspectorW || sz.Height < 600 {
				s.win.Resize(fyne.NewSize(1280, 800))
			}
			s.frame.relayout()
			s.updateCompact()
		})
	})
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
	// Render the turn immediately with a status footer; the backend either
	// starts it now or queues it, and the events reconcile the footer.
	ref := s.nextRef()
	s.addUserPending(text, ref)
	s.b.send(text, ref)
	s.win.Canvas().Focus(s.entry)
	s.scroll.ScrollToBottom()
}

// nextRef names one submitted message so its events can be reconciled with
// the transcript card that was rendered optimistically.
func (s *shell) nextRef() string {
	s.refSeq++
	return fmt.Sprintf("c%d", s.refSeq)
}

// relayoutChat re-runs the chat border layout after the approval dock's
// min-size changed. It deliberately skips Container.Refresh, which would
// re-layout (and re-wrap) every transcript block.
func (s *shell) relayoutChat() {
	if s.chatBody == nil {
		return
	}
	s.chatBody.Layout.Layout(s.chatBody.Objects, s.chatBody.Size())
}

// updateApprovalStatus surfaces pending approvals in the run-state line so a
// blocked run is not mistaken for a slow model.
func (s *shell) updateApprovalStatus() {
	if !s.cmp.running {
		return
	}
	if n := len(s.pendingApprovals); n > 0 {
		s.setRunState(fmt.Sprintf("Waiting for your approval (%d)", n), colorWarn, false)
		return
	}
	s.setRunState("Running…", colorWarn, false)
}

// setTextScale applies and persists a text-size multiplier. SetTheme repaints
// every window (Fyne re-layouts all renderers), so the change is live; the
// config save makes it survive restart. The config.changed event appends the
// user-facing notice.
func (s *shell) setTextScale(scale float64) {
	scale = config.ClampTextScale(scale)
	s.cfg.UI.TextScale = scale
	out := map[string]any{"type": "config.changed", "text_scale": scale}
	if err := s.cfg.Save(); err != nil {
		out["error"] = err.Error()
	}
	s.bus.Emit(out)
	applyTheme(s.app, s.cfg.UI.WithTextScale(scale))
	s.updateCompact()
}

// updateCompact drops the composer hint and secondary status chips when the
// window or the text is too large for everything to fit.
func (s *shell) updateCompact() {
	w := s.win.Canvas().Size().Width
	compact := (w > 0 && w < 1000) || s.cfg.UI.TextScale > 1.2
	s.cmp.setCompact(compact)
	if compact {
		s.tieChip.Hide()
		s.mcpChip.Hide()
	} else {
		if s.tieShown {
			s.tieChip.Show()
		}
		if s.mcpShown {
			s.mcpChip.Show()
		}
	}
}

// nudgeTextScale steps the text size by delta (A-/A+ buttons, Ctrl+= / Ctrl+-).
func (s *shell) nudgeTextScale(delta float64) {
	s.setTextScale(s.cfg.UI.TextScale + delta)
}

// setThemeMode applies and persists ui.theme (system/light/dark).
func (s *shell) setThemeMode(mode string) {
	s.cfg.UI.Theme = normMode(mode)
	applyTheme(s.app, s.cfg.UI)
	if err := s.cfg.Save(); err != nil {
		s.addNotice("⚠ theme save: " + err.Error())
	}
	s.addNotice("Theme: " + s.cfg.UI.ThemeMode())
}

// reloadPane re-reads the pane script from disk (or the embedded copy),
// recompiles it, and restarts the embedded runtime without touching the shell.
func (s *shell) reloadPane() {
	src, err := paneSource(s.cfg.UI.PaneDir)
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

// setWindowTitle reflects the session title in the OS window title.
func (s *shell) setWindowTitle() {
	// The project is part of the window title so several instances (one per
	// project) are distinguishable in the task bar.
	prefix := "Anneal"
	if project := previewBase(s.b.cwd()); project != "" {
		prefix += " — " + project
	}
	if s.title == "" {
		s.win.SetTitle(prefix)
		s.header.setTitle("New session")
		return
	}
	s.win.SetTitle(prefix + " — " + s.title)
	s.header.setTitle(trunc(s.title, 80))
}

// previewBase is the last path element ("" for an empty path).
func previewBase(path string) string {
	path = strings.TrimRight(path, "/")
	if path == "" {
		return ""
	}
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// refreshSessionMeta re-reads the current session's title (the runner sets it
// after the first run) and refreshes the sidebar.
func (s *shell) refreshSessionMeta() {
	for _, info := range s.b.sessions() {
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
