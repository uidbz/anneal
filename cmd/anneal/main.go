// Command anneal is the hybrid desktop shell: the stable, high-traffic UI is
// plain Go/Fyne, while experimental surfaces (the harness panel first) are
// Risor scripts embedded as gui.CanvasObject widgets.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image/color"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uidbz/anneal/assets"
	"github.com/uidbz/anneal/core/agent"
	coreapp "github.com/uidbz/anneal/core/app"
	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/improve"
	"github.com/uidbz/anneal/core/journey"
	"github.com/uidbz/anneal/core/paths"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// defaultVisibleBlocks bounds how many transcript widgets are laid out at
// once. Older blocks stay in memory and can be restored on demand.
const defaultVisibleBlocks = 300

// textScaleStep is the A-/A+ (Ctrl+= / Ctrl+-) increment for ui.text_scale.
const textScaleStep = 0.1

// streamChunkBytes bounds the mutable streaming label. A word-wrapped Fyne
// label re-lays out its whole text on every SetText, so letting one label grow
// to the size of a long answer freezes the UI (and the stop button) for
// seconds. Streamed text is frozen into immutable chunks instead; only the
// small tail label is re-laid out.
const streamChunkBytes = 2000

// todoPanelWidth keeps the todo sidebar readable instead of collapsing to the
// minimum width of its wrapped labels inside a Border east slot.
const todoPanelWidth = 260

func main() {
	rebuild := flag.Bool("rebuild-index", false, "re-index all sessions into tie and exit")
	checkUI := flag.Bool("check-ui", false, "compile the scripted pane and exit")
	checkHarness := flag.Bool("check-harness", false, "validate harness components and exit")
	scriptUI := flag.Bool("script-ui", false, "run a full-window scripted UI from ANNEAL_UI_DIR (e2e/dev)")
	checkScriptUI := flag.Bool("check-script-ui", false, "compile the ANNEAL_UI_DIR scripted UI and exit")
	sendTask := flag.String("send", "", "developer: auto-send this task after boot")
	quitAfterRun := flag.Bool("quit-after-run", false, "developer: quit after the auto-sent run finishes")
	flag.Parse()

	a, err := coreapp.New()
	if err != nil {
		log.Fatalf("anneal: %v", err)
	}
	paneSrc, err := paneSource(a.Cfg.UI.PaneDir)
	if err != nil {
		log.Fatalf("anneal: pane missing: %v", err)
	}
	if *scriptUI || *checkScriptUI {
		if err := runScriptUI(a, *checkScriptUI); err != nil {
			log.Fatalf("anneal: %v", err)
		}
		return
	}
	if *checkUI {
		if _, err := newScriptPane(nil, paneSrc,
			paneCapabilities{Events: true}, a.Bus, a.HarnessAPI(), paneEvent); err != nil {
			log.Fatalf("anneal: pane compile: %v", err)
		}
		fmt.Println("anneal: pane compiles")
		return
	}
	if *checkHarness {
		fmt.Println("anneal:", agent.FormatHarnessInfo(a.Runner.HarnessInfo()))
		return
	}
	if *rebuild {
		if err := a.Tie.Connect(); err != nil {
			log.Fatalf("anneal: tie connect: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		n, err := a.Runner.RebuildIndex(ctx)
		if err != nil {
			log.Fatalf("anneal: rebuild: %v", err)
		}
		fmt.Printf("anneal: re-indexed %d sessions\n", n)
		return
	}

	fyneApp := fyneapp.New()
	win := fyneApp.NewWindow("Anneal")
	sh, err := newShell(a, fyneApp, win, paneSrc)
	if err != nil {
		log.Fatalf("anneal: UI: %v", err)
	}
	sh.autoSend = *sendTask
	sh.autoQuit = *quitAfterRun
	win.SetContent(sh.root)
	win.Resize(fyne.NewSize(1100, 750))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sh.start(ctx)
	win.ShowAndRun()
}

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

	baseTheme fyne.Theme

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
		baseTheme:  &brandTheme{Theme: app.Settings().Theme()},
	}
	applyTextScale(app, s.baseTheme, a.Cfg.UI.TextScale)
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
	s.runDot.setStatus(okColor)
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

	sendBtn := widget.NewButton("send", s.submit)
	stopBtn := widget.NewButton("stop", func() {
		s.status.SetText("stopping…")
		s.a.Runner.Interrupt()
	})

	left := container.New(&rowLayout{gap: 8}, brand, s.modeBtn, newBtn, sessBtn,
		settingsBtn, s.improveBtn, fontDownBtn, fontUpBtn)
	rule := canvas.NewRectangle(color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x14})
	rule.SetMinSize(fyne.NewSize(0, 1))
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

// hgap is a transparent spacer (Fyne box layouts have no item spacing).
func hgap(w float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(w, 0))
	return r
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
func (s *shell) setRunState(text string, dot color.Color, alarm bool) {
	s.status.SetText(text)
	s.runDot.setStatus(dot)
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

// paneEvent keeps the scripted pane on a narrow event diet: harness/improve
// lifecycle only, never chat deltas or permission traffic.
func paneEvent(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	switch m["type"] {
	case "harness.warn", "harness.warning", "harness.promoted",
		"improve.done", "improve.library", "improve.error",
		"review.flag", "config.changed":
		return true
	default:
		return false
	}
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
	card := canvas.NewRectangle(color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x0a})
	card.CornerRadius = 8
	s.todoPane = container.NewStack(spacer,
		container.NewStack(card, container.New(&padLayout{l: 10, r: 10, t: 8, b: 8}, todoBody)))
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

// header/composer/root are set by build/wireLayout.

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

func (s *shell) slash(line string) {
	fields := strings.Fields(line)
	cmd := fields[0]
	switch cmd {
	case "/help":
		s.showText("commands", "/new /sessions /plan /build /stop /textsize /memory /skills /journey /activity /runs /index /learn /improve /harness /quit")
	case "/new":
		s.a.Runner.NewSession()
	case "/sessions":
		s.showSessions()
	case "/plan":
		s.a.Runner.SetMode("plan")
	case "/build":
		s.a.Runner.SetMode("default")
	case "/stop", "/interrupt":
		s.a.Runner.Interrupt()
	case "/textsize":
		s.textSizeCommand(fields[1:])
	case "/memory":
		m, _ := s.a.Mem.Read("memory")
		u, _ := s.a.Mem.Read("user")
		s.showText("memory", fmt.Sprintf("MEMORY.md (%d/%d):\n%s\n\nUSER.md (%d/%d):\n%s",
			len(m), s.a.Cfg.Memory.MemoryCharLimit, m,
			len(u), s.a.Cfg.Memory.UserCharLimit, u))
	case "/skills":
		var b strings.Builder
		list := s.a.Skills.List()
		if len(list) == 0 {
			b.WriteString("skills: none installed")
		}
		for _, sk := range list {
			fmt.Fprintf(&b, "- %s: %s\n", sk.Name, sk.Description)
		}
		s.showText("skills", b.String())
	case "/journey":
		var b strings.Builder
		for _, e := range s.a.Journey.Tail(25) {
			fmt.Fprintf(&b, "%s  %s\n", e.TS, e.Kind)
		}
		s.showText("journey", b.String())
	case "/activity", "/runs":
		s.refreshActivity()
		s.tabs.SelectIndex(2)
	case "/index":
		go func() {
			n, err := s.a.Runner.RebuildIndex(context.Background())
			msg := map[string]any{"type": "tie.rebuilt", "sessions": n, "error": ""}
			if err != nil {
				msg["error"] = err.Error()
			}
			s.a.Bus.Emit(msg)
		}()
		s.addNotice("index rebuild requested")
	case "/learn":
		s.a.Review.LearnNow()
		s.addNotice("learning pass queued")
	case "/improve":
		s.improveNow()
	case "/harness":
		s.tabs.SelectIndex(1)
	case "/quit":
		s.app.Quit()
	default:
		s.addNotice("unknown command " + cmd + " — try /help")
	}
}

func (s *shell) showSessions() {
	infos := s.a.Runner.Sessions()
	var d dialog.Dialog
	list := widget.NewList(
		func() int { return len(infos) },
		func() fyne.CanvasObject { return widget.NewButton("", nil) },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			info := infos[i]
			b := o.(*widget.Button)
			title := info.Title
			if title == "" {
				title = info.ID
			}
			if len(title) > 60 {
				title = title[:60] + "…"
			}
			b.SetText(fmt.Sprintf("%s  (%s)", title, info.Updated))
			b.OnTapped = func() {
				s.a.Runner.OpenSession(info.ID)
				if d != nil {
					d.Hide()
				}
			}
		},
	)
	d = dialog.NewCustom("Sessions", "Close", list, s.win)
	d.Resize(fyne.NewSize(560, 420))
	d.Show()
}

func (s *shell) showText(title, body string) {
	lbl := widget.NewLabel(body)
	lbl.Wrapping = fyne.TextWrapWord
	lbl.Selectable = true
	sc := container.NewVScroll(lbl)
	sc.SetMinSize(fyne.NewSize(640, 420))
	d := dialog.NewCustom(title, "Close", sc, s.win)
	d.Show()
}

// refreshActivity rebuilds the Activity tab from the journey log and the
// improvement records on disk. It is deliberately a plain, refresh-on-demand
// view: no model calls and no background watchers.
func (s *shell) refreshActivity() {
	if s.activity == nil {
		return
	}
	s.activity.RemoveAll()
	s.activity.Add(container.NewBorder(nil, nil, nil,
		widget.NewButton("refresh", s.refreshActivity), nil))
	s.activity.Add(widget.NewLabelWithStyle("Journey (newest last)", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	journey := s.a.Journey.Tail(40)
	if len(journey) == 0 {
		empty := widget.NewLabel("empty — memory/skill/review/improve events land here")
		empty.Importance = widget.LowImportance
		s.activity.Add(empty)
	}
	for _, e := range journey {
		lbl := widget.NewLabel(journeyLine(e))
		lbl.Wrapping = fyne.TextWrapWord
		lbl.Selectable = true
		s.activity.Add(lbl)
	}
	s.activity.Add(widget.NewSeparator())
	s.activity.Add(widget.NewLabelWithStyle("Improvement runs", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	runs := loadImprovementRuns(20)
	if len(runs) == 0 {
		hint := widget.NewLabel("none yet — improve runs are recorded under " + paths.Improvements())
		hint.Importance = widget.LowImportance
		s.activity.Add(hint)
	}
	for _, r := range runs {
		title := fmt.Sprintf("%s — k=%d mode=%s", r.RunID, r.K, r.Mode)
		if title == "" {
			title = r.Dir
		}
		s.activity.Add(widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		body := r.Task
		if r.Note != "" {
			body += "\nnote: " + r.Note
		}
		if r.Successor != "" {
			body += "\noverlay: " + r.Successor
		}
		if r.LibraryEntry != "" {
			body += "\nlibrary: " + r.LibraryEntry
		}
		for _, c := range r.Candidates {
			state := "probed"
			if c.Skipped != "" {
				state = "skipped: " + c.Skipped
			} else if c.Probe != nil {
				state = fmt.Sprintf("probed: %d steps", len(c.Probe.Steps))
			}
			body += fmt.Sprintf("\n- %s: %s", c.Kind, state)
		}
		if r.Decisions != "" {
			body += "\ndecisions: " + shortText(oneLine(r.Decisions), 500)
		}
		lbl := widget.NewLabel(body)
		lbl.Wrapping = fyne.TextWrapWord
		lbl.Selectable = true
		s.activity.Add(lbl)
		s.activity.Add(widget.NewSeparator())
	}
	s.activity.Refresh()
}

// journeyLine formats one journey record as a readable line for the
// Activity tab (raw attr maps become kind-specific phrasing).
func journeyLine(e journey.Entry) string {
	ts := e.TS
	if len(ts) >= 16 {
		ts = ts[:10] + " " + ts[11:16]
	}
	txt := ts + "  " + e.Kind
	switch e.Kind {
	case "review":
		if v, ok := e.Attrs["lessons"]; ok {
			txt += fmt.Sprintf(" · %v lessons", v)
		}
	case "improve":
		if e.Attrs["successor"] == true {
			txt += " · successor applied"
		} else {
			txt += " · kept current harness"
		}
		if r, ok := e.Attrs["run"]; ok {
			txt += " (run " + fmt.Sprint(r) + ")"
		}
	case "review-flag":
		txt += " · " + shortText(oneLine(str(e.Attrs["gap"])), 120)
	default:
		var parts []string
		for k, v := range e.Attrs {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
		sort.Strings(parts)
		if len(parts) > 0 {
			txt += " · " + strings.Join(parts, " ")
		}
	}
	return txt
}

// loadImprovementRuns reads step.json records, newest first. Corrupt or
// partial records are skipped rather than failing the view.
func loadImprovementRuns(limit int) []improve.Result {
	entries, err := os.ReadDir(paths.Improvements())
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	out := make([]improve.Result, 0, len(names))
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(paths.Improvements(), name, "step.json"))
		if err != nil {
			continue
		}
		var r improve.Result
		if json.Unmarshal(b, &r) != nil || r.RunID == "" {
			continue
		}
		out = append(out, r)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *shell) showSettings() {
	b, err := json.MarshalIndent(s.a.Cfg, "", "  ")
	if err != nil {
		s.addNotice("⚠ settings: " + err.Error())
		return
	}
	ed := widget.NewMultiLineEntry()
	ed.SetText(string(b))
	ed.Wrapping = fyne.TextWrapOff
	sc := container.NewVScroll(ed)
	sc.SetMinSize(fyne.NewSize(720, 480))
	d := dialog.NewCustomConfirm("Settings — ~/.anneal/config.json", "Save", "Cancel", sc,
		func(ok bool) {
			if !ok {
				return
			}
			if err := s.a.SaveConfig(ed.Text); err != nil {
				s.addNotice("⚠ settings: " + err.Error())
				return
			}
			s.addNotice("✔ settings saved")
		}, s.win)
	d.Show()
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
	applyTextScale(s.app, s.baseTheme, scale)
}

// nudgeTextScale steps the text size by delta (A-/A+ buttons, Ctrl+= / Ctrl+-).
func (s *shell) nudgeTextScale(delta float64) {
	s.setTextScale(s.a.Cfg.UI.TextScale + delta)
}

// textSizeCommand implements /textsize:
//
//	/textsize             show the current size
//	/textsize 120%        absolute percentage
//	/textsize +10%        relative percentage points
//	/textsize 1.2         absolute multiplier
//	/textsize reset       back to 100%
func (s *shell) textSizeCommand(args []string) {
	if len(args) == 0 {
		s.addNotice(fmt.Sprintf("text size: %.0f%% (range %.0f–%.0f%%)",
			s.a.Cfg.UI.TextScale*100, config.MinTextScale*100, config.MaxTextScale*100))
		return
	}
	arg := strings.TrimSpace(strings.ToLower(args[0]))
	switch arg {
	case "reset", "default", "normal":
		s.setTextScale(1.0)
		return
	}
	percent := strings.HasSuffix(arg, "%")
	raw := strings.TrimSuffix(arg, "%")
	relative := strings.HasPrefix(raw, "+") || strings.HasPrefix(raw, "-")
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		s.addNotice("⚠ /textsize expects e.g. 120%, +10%, 1.2, +0.1, or reset")
		return
	}
	scale := v
	switch {
	case percent:
		scale = v / 100
		if relative {
			scale += s.a.Cfg.UI.TextScale
		}
	case relative:
		scale = s.a.Cfg.UI.TextScale + v
	}
	s.setTextScale(scale)
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

func (s *shell) addUser(text string) {
	lbl := widget.NewLabel("> " + text)
	lbl.Wrapping = fyne.TextWrapWord
	lbl.TextStyle = fyne.TextStyle{Italic: true}
	lbl.Selectable = true
	// Thin accent bar marks the user's own turns in the transcript.
	bar := canvas.NewRectangle(accent)
	bar.SetMinSize(fyne.NewSize(3, 0))
	s.appendBlock(container.NewBorder(nil, nil, bar, nil, lbl))
}

func (s *shell) addMarkdown(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	// RichText is not selectable in Fyne, so the transcript uses plain
	// selectable labels: prose with markdown stripped, tables/code as
	// monospace blocks with intact alignment.
	for _, seg := range splitMarkdownSegments(text) {
		lbl := widget.NewLabel(seg.text)
		lbl.Wrapping = fyne.TextWrapWord
		lbl.Selectable = true
		if seg.mono {
			lbl.TextStyle = fyne.TextStyle{Monospace: true}
		}
		s.appendBlock(lbl)
	}
}

func (s *shell) addNotice(text string) {
	lbl := widget.NewLabel(text)
	lbl.Wrapping = fyne.TextWrapWord
	lbl.Importance = widget.LowImportance
	lbl.Selectable = true
	s.appendBlock(lbl)
}

func (s *shell) addTool(text string) {
	lbl := widget.NewLabel(text)
	lbl.Wrapping = fyne.TextWrapWord
	lbl.TextStyle = fyne.TextStyle{Monospace: true}
	lbl.Importance = widget.LowImportance
	lbl.Selectable = true
	s.appendBlock(lbl)
}

func (s *shell) appendBlock(obj fyne.CanvasObject) {
	// The streaming tail is always visually last.
	if s.tail != nil {
		s.chatBox.Remove(s.tail)
	}
	s.visible = append(s.visible, obj)
	s.chatBox.Add(obj)
	if s.tail != nil {
		s.chatBox.Add(s.tail)
	}
	s.trimVisible()
	s.chatBox.Refresh()
	if s.scroll != nil {
		s.scroll.ScrollToBottom()
	}
}

// trimVisible bounds the rendered transcript. Older blocks are detached from
// the layout (not discarded) and can be restored with "show earlier messages",
// so long sessions do not re-layout thousands of widgets on every event.
func (s *shell) trimVisible() {
	if s.maxVisible <= 0 {
		return
	}
	for len(s.visible) > s.maxVisible {
		obj := s.visible[0]
		s.visible = s.visible[1:]
		s.hidden = append(s.hidden, obj)
		s.chatBox.Remove(obj)
	}
	s.updateEarlier()
}

func (s *shell) updateEarlier() {
	if s.earlier == nil {
		return
	}
	if len(s.hidden) == 0 {
		s.earlier.Hide()
		return
	}
	s.earlier.SetText(fmt.Sprintf("show %d earlier messages", len(s.hidden)))
	s.earlier.Show()
}

func (s *shell) showEarlier() {
	if len(s.hidden) == 0 {
		return
	}
	n := s.maxVisible
	if n <= 0 || n > len(s.hidden) {
		n = len(s.hidden)
	}
	restore := s.hidden[len(s.hidden)-n:]
	s.hidden = s.hidden[:len(s.hidden)-n]
	s.visible = append(append([]fyne.CanvasObject{}, restore...), s.visible...)
	s.chatBox.RemoveAll()
	for _, obj := range s.visible {
		s.chatBox.Add(obj)
	}
	for _, obj := range s.streamBlocks {
		s.chatBox.Add(obj)
	}
	if s.tail != nil {
		s.chatBox.Add(s.tail)
	}
	s.chatBox.Refresh()
	s.updateEarlier()
	if s.scroll != nil {
		s.scroll.ScrollToTop()
	}
}

func (s *shell) resetChat() {
	s.tail = nil
	s.tailBuf = ""
	s.streamBlocks = nil
	s.visible = nil
	s.hidden = nil
	s.chatBox.RemoveAll()
	s.chatBox.Refresh()
	if s.earlier != nil {
		s.earlier.Hide()
	}
	s.perms.RemoveAll()
	s.perms.Refresh()
	s.permRows = map[string]fyne.CanvasObject{}
	s.todos.RemoveAll()
	s.todos.Refresh()
	if s.todoPane != nil {
		s.todoPane.Hide()
	}
}

func (s *shell) ensureTail() {
	if s.tail == nil {
		s.tailBuf = ""
		s.tail = widget.NewLabel("")
		s.tail.Wrapping = fyne.TextWrapWord
		s.chatBox.Add(s.tail)
		s.chatBox.Refresh()
	}
}

// flushTail renders the buffered stream text. Once the tail reaches
// streamChunkBytes it is frozen (the label is left in place and a fresh tail
// starts), so no single label ever gets large enough for its word-wrap layout
// to block the UI thread. Frozen chunks are temporary and removed by
// closeTail, which replaces them with the final selectable block.
func (s *shell) flushTail() {
	if s.tail == nil {
		return
	}
	for len(s.tailBuf) >= streamChunkBytes {
		cut := chunkCut(s.tailBuf, streamChunkBytes)
		chunk, rest := s.tailBuf[:cut], s.tailBuf[cut:]
		s.tail.SetText(chunk)
		s.tail.Refresh()
		s.streamBlocks = append(s.streamBlocks, s.tail)
		s.tail = widget.NewLabel(rest)
		s.tail.Wrapping = fyne.TextWrapWord
		s.tailBuf = rest
		s.chatBox.Add(s.tail)
	}
	s.tail.SetText(s.tailBuf)
	s.tail.Refresh()
	s.chatBox.Refresh()
	if s.scroll != nil {
		s.scroll.ScrollToBottom()
	}
}

// chunkCut picks a chunk boundary near max, preferring a line or word break
// and never splitting a UTF-8 rune.
func chunkCut(s string, max int) int {
	cut := max
	if i := strings.LastIndexByte(s[:cut], '\n'); i > cut/2 {
		cut = i + 1
	} else if i := strings.LastIndexByte(s[:cut], ' '); i > cut/2 {
		cut = i + 1
	}
	for cut > 0 && cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut <= 0 {
		return max
	}
	return cut
}

func (s *shell) closeTail(final string) {
	removed := false
	if s.tail != nil {
		s.chatBox.Remove(s.tail)
		s.tail = nil
		removed = true
	}
	for _, obj := range s.streamBlocks {
		s.chatBox.Remove(obj)
		removed = true
	}
	s.tailBuf = ""
	s.streamBlocks = nil
	if removed {
		s.chatBox.Refresh()
	}
	if strings.TrimSpace(final) != "" {
		s.addMarkdown(final)
	}
}

func (s *shell) handleEvent(e map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "anneal: ui event panic: %v\n", r)
		}
	}()
	switch str(e["type"]) {
	case "provider.state":
		h := obj(e["health"])
		state := str(h["state"])
		switch state {
		case "online":
			s.modelLbl.SetText(fmt.Sprintf("%s · %s ctx",
				trunc(str(h["model"]), 22), humanK(int(num(h["ctx_window"])))))
			s.modelDot.setStatus(okColor)
		case "connecting", "probing":
			s.modelLbl.SetText("model: " + state + "…")
			s.modelDot.setStatus(warnColor)
		default:
			msg := str(h["error"])
			if msg != "" {
				s.modelLbl.SetText("model: " + state + " — " + msg)
			} else {
				s.modelLbl.SetText("model: " + state)
			}
			s.modelDot.setStatus(errColor)
		}
		s.chipModel.Show()
	case "tie.state":
		st := obj(e["state"])
		switch {
		case !boolean(st["enabled"]):
			s.chipTie.Hide()
		case boolean(st["online"]):
			s.tie.SetText("index: " + str(st["collection"]))
			s.tieDot.setStatus(okColor)
			s.chipTie.Show()
		default:
			s.tie.SetText("index: offline")
			s.tieDot.setStatus(errColor)
			s.chipTie.Show()
		}
	case "mcp.state":
		servers := list(e["servers"])
		up, tools := 0, 0
		for _, raw := range servers {
			srv := obj(raw)
			if boolean(srv["connected"]) {
				up++
			}
			tools += int(num(srv["tools"]))
		}
		if len(servers) > 0 {
			s.mcp.SetText(fmt.Sprintf("mcp: %d/%d · %d tools", up, len(servers), tools))
			switch {
			case up == len(servers):
				s.mcpDot.setStatus(okColor)
			case up > 0:
				s.mcpDot.setStatus(warnColor)
			default:
				s.mcpDot.setStatus(errColor)
			}
			s.chipMCP.Show()
		} else {
			s.chipMCP.Hide()
		}
	case "run.started":
		s.setRunState("running…", warnColor, false)
		s.usage.SetText("running…")
	case "run.busy":
		s.status.SetText("a run is already active")
	case "text.delta":
		s.ensureTail()
		s.tailBuf += str(e["text"])
		s.flushTail()
	case "thinking.delta":
		s.status.SetText("thinking…")
		s.runDot.setStatus(warnColor)
	case "message.final":
		s.closeTail(str(e["text"]))
	case "tool.start":
		s.closeTail("")
		s.addTool(fmt.Sprintf("🔧 %s %s", str(e["name"]), oneLine(str(e["input"]))))
	case "tool.result":
		mark := "✔"
		if boolean(e["error"]) {
			mark = "✖"
		}
		s.addTool(fmt.Sprintf("  %s %s: %s", mark, str(e["name"]), oneLine(str(e["preview"]))))
	case "run.finished":
		s.closeTail("")
		u := obj(e["usage"])
		s.usage.SetText(fmt.Sprintf("in %s · out %s · ctx %d%%",
			humanK(int(num(u["tin"]))), humanK(int(num(u["tout"]))), int(num(e["ctx_pct"]))))
		switch {
		case boolean(e["stopped"]):
			s.setRunState("stopped", warnColor, false)
		case boolean(e["ok"]):
			s.setRunState("ready", okColor, false)
		default:
			s.setRunState("error", errColor, true)
			s.usage.SetText("")
			if msg := str(e["error"]); msg != "" {
				s.addNotice("⚠ " + msg)
			}
		}
		s.scroll.ScrollToBottom()
		if s.autoQuit {
			time.AfterFunc(1500*time.Millisecond, func() { fyne.Do(func() { s.app.Quit() }) })
		}
	case "session.started":
		s.resetChat()
		s.addNotice("new session — describe a task below, or /help for commands")
		s.win.SetTitle("Anneal")
		s.entry.SetText("")
		s.win.Canvas().Focus(s.entry)
	case "session.opened":
		s.resetChat()
		for _, raw := range list(e["blocks"]) {
			s.renderBlock(obj(raw))
		}
		title := str(e["title"])
		if title != "" {
			s.win.SetTitle("Anneal — " + title)
		}
		s.scroll.ScrollToBottom()
	case "session.error":
		s.addNotice(fmt.Sprintf("⚠ session %s: %s", str(e["id"]), str(e["error"])))
	case "mode.changed":
		s.mode = str(e["mode"])
		s.modeBtn.SetText(modeTitle(s.mode))
	case "todo.updated":
		s.renderTodos(list(e["items"]))
	case "perm.request":
		s.addPerm(e)
	case "perm.saved":
		s.addNotice(fmt.Sprintf("🔐 saved permission: %s %s", str(e["action"]),
			strings.Join(stringList(e["resources"]), ", ")))
	case "perm.warning":
		s.addNotice("⚠ permission save failed: " + str(e["error"]))
	case "perm.resolved":
		s.removeCard(str(e["id"]))
	case "memory.stage":
		op := obj(e["op"])
		id := str(op["id"])
		info := fmt.Sprintf("💾 memory[%s] %s via %s\n%s", str(op["target"]),
			str(op["action"]), str(op["source"]), memoryOpText(op))
		s.addApprovalCard(id, info,
			widget.NewButton("approve", func() { s.a.RespondMemory(id, "approve") }),
			widget.NewButton("reject", func() { s.a.RespondMemory(id, "reject") }),
		)
	case "skill.stage":
		op := obj(e["op"])
		id := str(op["id"])
		info := fmt.Sprintf("🛠 skill[%s] %s via %s\n%s", str(op["action"]),
			str(op["name"]), str(op["source"]), str(e["preview"]))
		s.addApprovalCard(id, info,
			widget.NewButton("approve", func() { s.a.RespondSkill(id, "approve") }),
			widget.NewButton("reject", func() { s.a.RespondSkill(id, "reject") }),
		)
	case "compaction.done":
		s.addNotice(fmt.Sprintf("🗜 context compacted: %d → %d tokens (%d msgs)",
			int(num(e["before"])), int(num(e["after"])), int(num(e["msgs"]))))
	case "review.start":
		s.setRunState("reviewing…", warnColor, false)
		s.addNotice("🧠 background review started")
	case "review.skipped":
		s.setRunState("ready", okColor, false)
		if str(e["reason"]) != "preempted by user" {
			s.addNotice("🧠 review skipped: " + str(e["reason"]))
		}
	case "review.flag":
		s.addNotice("🪧 reviewer flagged a harness gap: " + str(e["gap"]))
		s.refreshActivity()
	case "review.done":
		s.setRunState("ready", okColor, false)
		ledger := list(e["ledger"])
		switch {
		case str(e["error"]) != "":
			s.addNotice("⚠ background review: " + str(e["error"]))
		case len(ledger) == 0:
			s.addNotice("🧠 review: nothing worth saving")
		default:
			s.addNotice(fmt.Sprintf("🧠 review saved %d lessons", len(ledger)))
		}
		s.refreshActivity()
	case "improve.auto":
		s.improveAuto = true
		s.addNotice("⚡ auto-improve: idle after a successful run — improving the harness once for this session")
	case "improve.start":
		s.setRunState("improving harness…", warnColor, false)
		s.improveBtn.SetText("improving…")
		tag := ""
		if s.improveAuto {
			tag = " (auto)"
		}
		s.addNotice("🔧 harness improvement started" + tag + " (run " + str(e["run"]) + ")")
	case "improve.propose":
		s.addNotice("🔧 propose: " + str(e["kind"]) + " editor drafting…")
	case "improve.probe":
		s.addNotice(fmt.Sprintf("🔧 probe %s: %d steps in %ds",
			str(e["kind"]), int(num(e["steps"])), int(num(e["duration_s"]))))
	case "improve.compose":
		s.addNotice("🔧 composed successor")
	case "improve.done":
		s.setRunState("ready", okColor, false)
		s.improveBtn.SetText("improve")
		s.improveAuto = false
		switch {
		case !boolean(e["ok"]):
			s.addNotice("⚠ improve: " + str(e["error"]))
		case !boolean(e["successor"]):
			s.addNotice("🔧 improve: composer kept the current harness")
		default:
			s.addNotice("🔧 improve: successor applied to this session")
		}
		s.refreshActivity()
	case "improve.library":
		if str(e["entry"]) != "" {
			s.addNotice("🔧 improvement library entry: " + str(e["entry"]))
		} else if str(e["error"]) != "" {
			s.addNotice("⚠ improvement library: " + str(e["error"]))
		}
		s.refreshActivity()
	case "improve.error":
		s.setRunState("ready", warnColor, false)
		s.improveBtn.SetText("improve")
		s.improveAuto = false
		s.addNotice("⚠ improve: " + str(e["error"]))
		s.refreshActivity()
	case "memory.updated":
		s.addNotice(fmt.Sprintf("💾 memory %s: %s", str(e["target"]), str(e["action"])))
	case "memory.resolved":
		s.removeCard(str(e["id"]))
		if str(e["error"]) != "" {
			s.addNotice("⚠ memory approval: " + str(e["error"]))
		} else if boolean(e["approved"]) {
			s.addNotice("✔ memory saved")
		}
	case "skill.applied":
		s.addNotice(fmt.Sprintf("✔ skill %s: %s", str(e["action"]), str(e["name"])))
	case "skill.resolved":
		s.removeCard(str(e["id"]))
		if str(e["error"]) != "" {
			s.addNotice("⚠ skill approval: " + str(e["error"]))
		}
	case "harness.warning":
		s.addNotice("⚠ harness: " + str(e["message"]))
	case "harness.warn":
		s.addNotice("⚠ harness files changed — prose applies next run")
		s.refreshActivity()
	case "harness.promoted":
		if str(e["error"]) != "" {
			s.addNotice("⚠ harness promote: " + str(e["error"]))
		} else {
			s.addNotice("✔ harness promoted (restart for guardrail rules)")
		}
		s.refreshActivity()
	case "config.changed":
		// Covers edits from the settings dialog, scripts (agent.saveConfig),
		// and setTextScale; re-applying the theme is idempotent.
		applyTextScale(s.app, s.baseTheme, s.a.Cfg.UI.TextScale)
		switch {
		case str(e["error"]) != "":
			s.addNotice("⚠ settings save: " + str(e["error"]))
		case e["text_scale"] != nil:
			s.addNotice(fmt.Sprintf("⚙ text size: %.0f%%", num(e["text_scale"])*100))
		default:
			s.addNotice("⚙ configuration changed")
		}
	case "skill.error":
		s.addNotice("⚠ skill loader: " + str(e["error"]))
	case "tie.rebuilt":
		if str(e["error"]) != "" {
			s.addNotice("⚠ index rebuild: " + str(e["error"]))
		} else {
			s.addNotice(fmt.Sprintf("✔ index rebuilt: %d sessions", int(num(e["sessions"]))))
		}
	}
}

func (s *shell) renderBlock(b map[string]any) {
	switch str(b["kind"]) {
	case "user":
		s.addUser(str(b["text"]))
	case "assistant":
		s.addMarkdown(str(b["text"]))
	case "tool":
		s.addTool("🔧 " + str(b["name"]) + " " + oneLine(str(b["input"])))
	case "toolres":
		mark := "✔"
		if boolean(b["error"]) {
			mark = "✖"
		}
		s.addTool("  " + mark + " " + str(b["name"]) + ": " + oneLine(str(b["text"])))
	}
}

func (s *shell) renderTodos(items []any) {
	s.todos.RemoveAll()
	for _, raw := range items {
		it := obj(raw)
		glyph := "☐"
		switch str(it["status"]) {
		case "in_progress":
			glyph = "▶"
		case "done":
			glyph = "☑"
		case "cancelled":
			glyph = "✖"
		}
		lbl := widget.NewLabel(glyph + " " + str(it["content"]))
		lbl.Wrapping = fyne.TextWrapWord
		lbl.Selectable = true
		s.todos.Add(lbl)
	}
	if s.todoPane != nil {
		if len(items) == 0 {
			s.todoPane.Hide()
		} else {
			s.todoPane.Show()
		}
		s.todoPane.Refresh()
	}
	s.todos.Refresh()
}

func (s *shell) addPerm(e map[string]any) {
	id := str(e["id"])
	action := str(e["action"])
	if action == "" {
		action = str(e["tool"])
	}
	if resources := strings.Join(stringList(e["resources"]), ", "); resources != "" {
		action += " [" + resources + "]"
	}
	action = oneLine(action)
	grant := ""
	if g := str(e["grant"]); g != "" {
		grant = "\nalways: " + g
	}
	info := fmt.Sprintf("🔐 %s — %s%s\n%s", action, str(e["message"]), grant,
		oneLine(str(e["input"])))
	s.addApprovalCard(id, info,
		widget.NewButton("once", func() { s.a.Runner.RespondPerm(id, "allow") }),
		widget.NewButton("always", func() { s.a.Runner.RespondPerm(id, "session") }),
		widget.NewButton("reject", func() { s.a.Runner.RespondPerm(id, "deny") }),
	)
}

func (s *shell) addApprovalCard(id, info string, buttons ...fyne.CanvasObject) {
	lbl := widget.NewLabel(info)
	lbl.Wrapping = fyne.TextWrapWord
	lbl.Selectable = true
	// Card with an amber wash (attention) and the buttons on their own row.
	card := canvas.NewRectangle(color.NRGBA{R: 0xf5, G: 0x9e, B: 0x0b, A: 0x14})
	card.CornerRadius = 8
	body := container.NewBorder(nil,
		container.New(&rowLayout{gap: 8}, buttons...), nil, nil, lbl)
	stack := container.NewStack(card, container.New(&padLayout{l: 12, r: 12, t: 8, b: 8}, body))
	gap := canvas.NewRectangle(color.Transparent)
	gap.SetMinSize(fyne.NewSize(0, 8))
	s.permRows[id] = container.NewVBox(stack, gap)
	s.perms.Add(s.permRows[id])
	s.perms.Refresh()
}

func (s *shell) removeCard(id string) {
	row, ok := s.permRows[id]
	if !ok {
		return
	}
	s.perms.Remove(row)
	delete(s.permRows, id)
	s.perms.Refresh()
}

func memoryOpText(op map[string]any) string {
	switch str(op["action"]) {
	case "add":
		return "+ " + str(op["content"])
	case "patch":
		return shortText(str(op["old"]), 60) + "  →  " + shortText(str(op["new"]), 60)
	default:
		return "- " + shortText(str(op["old"]), 60)
	}
}

func shortText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func modeTitle(mode string) string {
	if mode == "plan" {
		return "📋 plan"
	}
	return "🛠 build"
}

func oneLine(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}

// normalizeEvent converts any event value (maps, review.Event structs, nested
// structs) into a JSON-shaped map so the UI has one consistent value model.
func normalizeEvent(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		// Core events are already maps, but nested values may be structs.
		b, err := json.Marshal(m)
		if err == nil {
			var out map[string]any
			if json.Unmarshal(b, &out) == nil {
				return out
			}
		}
		return m
	}
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

func boolean(v any) bool {
	b, _ := v.(bool)
	return b
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func stringList(v any) []string {
	items := list(v)
	if items == nil {
		if values, ok := v.([]string); ok {
			return values
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, str(item))
	}
	return out
}

// paneSource prefers an on-disk pane script (config `ui.pane_dir`, then the
// full script-UI dir for co-located development) and falls back to the
// embedded copy.
func paneSource(configDir string) (string, error) {
	dirs := []string{
		configDir,
		os.Getenv("ANNEAL_PANE_DIR"),
		os.Getenv("ANNEAL_UI_DIR"),
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if b, err := os.ReadFile(dir + "/harness.risor"); err == nil {
			return string(b), nil
		}
	}
	b, err := assets.UI.ReadFile("ui/harness.risor")
	return string(b), err
}
