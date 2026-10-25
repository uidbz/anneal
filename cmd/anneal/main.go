// Command anneal is the hybrid desktop shell: the stable, high-traffic UI is
// plain Go/Fyne, while experimental surfaces (the harness panel first) are
// Risor scripts embedded as gui.CanvasObject widgets.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/uidbz/anneal/assets"
	"github.com/uidbz/anneal/core/agent"
	coreapp "github.com/uidbz/anneal/core/app"
	"github.com/uidbz/anneal/core/improve"
	"github.com/uidbz/anneal/core/paths"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// defaultVisibleBlocks bounds how many transcript widgets are laid out at
// once. Older blocks stay in memory and can be restored on demand.
const defaultVisibleBlocks = 300

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

	status   *widget.Label
	usage    *widget.Label
	tie      *widget.Label
	mcp      *widget.Label
	modeBtn  *widget.Button
	entry    *widget.Entry
	scroll   *container.Scroll
	chatBox  *fyne.Container // visible transcript blocks (append-only)
	earlier  *widget.Button  // "show earlier messages" when the window is capped
	visible  []fyne.CanvasObject
	hidden   []fyne.CanvasObject
	perms    *fyne.Container
	todos    *fyne.Container
	activity *fyne.Container
	tabs     *container.AppTabs
	harness  *scriptPane
	paneSrc  string
	permRows map[string]fyne.CanvasObject

	header   fyne.CanvasObject
	composer fyne.CanvasObject
	root     fyne.CanvasObject

	tail       *widget.Label
	tailBuf    string
	mode       string
	autoSend   string
	autoQuit   bool
	maxVisible int
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
	s.build()
	if err := s.buildHarness(paneSrc); err != nil {
		return nil, err
	}
	s.wireLayout()
	return s, nil
}

func (s *shell) build() {
	s.status = widget.NewLabel("model server: connecting…")
	s.usage = widget.NewLabel("")
	s.tie = widget.NewLabel("")
	s.mcp = widget.NewLabel("")
	s.status.Wrapping = fyne.TextWrapWord

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
	stopBtn := widget.NewButton("stop", func() { s.a.Runner.Interrupt() })

	s.header = container.NewVBox(
		container.NewHBox(s.modeBtn, newBtn, sessBtn, settingsBtn, widget.NewSeparator(), s.status),
		container.NewHBox(s.usage, s.tie, s.mcp),
	)
	s.composer = container.NewBorder(nil, nil, nil,
		container.NewHBox(sendBtn, stopBtn), s.entry)
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
	s.scroll = container.NewVScroll(container.NewVBox(s.earlier, s.chatBox))
	chat := container.NewBorder(
		container.NewVBox(s.perms),
		nil, nil,
		container.NewVBox(widget.NewLabelWithStyle("todos", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), s.todos),
		s.scroll,
	)
	s.tabs = container.NewAppTabs(
		container.NewTabItem("Chat", chat),
		container.NewTabItem("Harness", s.harness.view),
		container.NewTabItem("Activity", container.NewVScroll(s.activity)),
	)
	s.root = container.NewBorder(s.header, s.composer, nil, nil, s.tabs)
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
		s.showText("commands", "/new /sessions /plan /build /stop /memory /skills /journey /activity /runs /index /learn /improve /harness /quit")
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
		s.addNotice("harness improvement started")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
			defer cancel()
			if _, err := s.a.Improve(ctx); err != nil {
				s.a.Bus.Emit(map[string]any{"type": "improve.error", "error": err.Error()})
			}
		}()
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
	s.activity.Add(widget.NewButton("refresh", s.refreshActivity))
	s.activity.Add(widget.NewLabelWithStyle("Journey (newest last)", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	journey := s.a.Journey.Tail(40)
	if len(journey) == 0 {
		s.activity.Add(widget.NewLabel("empty"))
	}
	for _, e := range journey {
		lbl := widget.NewLabel(fmt.Sprintf("%s  %s  %v", e.TS, e.Kind, e.Attrs))
		lbl.Wrapping = fyne.TextWrapWord
		s.activity.Add(lbl)
	}
	s.activity.Add(widget.NewSeparator())
	s.activity.Add(widget.NewLabelWithStyle("Improvement runs", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	runs := loadImprovementRuns(20)
	if len(runs) == 0 {
		s.activity.Add(widget.NewLabel("none yet — /improve creates records under ~/.anneal/improvements/"))
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
		s.activity.Add(lbl)
		s.activity.Add(widget.NewSeparator())
	}
	s.activity.Refresh()
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
	add(fyne.KeyL, fyne.KeyModifierControl, func() { s.win.Canvas().Focus(s.entry) })
	add(fyne.KeyR, fyne.KeyModifierControl, s.reloadPane)
	add(fyne.KeyQ, fyne.KeyModifierControl, func() { s.app.Quit() })
	add(fyne.Key1, fyne.KeyModifierControl, func() { s.tabs.SelectIndex(0) })
	add(fyne.Key2, fyne.KeyModifierControl, func() { s.tabs.SelectIndex(1) })
	add(fyne.Key3, fyne.KeyModifierControl, func() { s.tabs.SelectIndex(2) })
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
	s.appendBlock(lbl)
}

func (s *shell) addMarkdown(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	rt := widget.NewRichTextFromMarkdown(text)
	rt.Wrapping = fyne.TextWrapWord
	s.appendBlock(rt)
}

func (s *shell) addNotice(text string) {
	lbl := widget.NewLabel(text)
	lbl.Wrapping = fyne.TextWrapWord
	lbl.Importance = widget.LowImportance
	s.appendBlock(lbl)
}

func (s *shell) addTool(text string) {
	lbl := widget.NewLabel(text)
	lbl.Wrapping = fyne.TextWrapWord
	lbl.TextStyle = fyne.TextStyle{Monospace: true}
	lbl.Importance = widget.LowImportance
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

func (s *shell) flushTail() {
	if s.tail == nil {
		return
	}
	s.tail.SetText(s.tailBuf)
	s.chatBox.Refresh()
	s.scroll.ScrollToBottom()
}

func (s *shell) closeTail(final string) {
	if s.tail != nil {
		s.chatBox.Remove(s.tail)
		s.tail = nil
		s.tailBuf = ""
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
		if state == "online" {
			s.status.SetText(fmt.Sprintf("model: %s · ctx: %d", str(h["model"]), int(num(h["ctx_window"]))))
		} else if str(h["error"]) != "" {
			s.status.SetText(fmt.Sprintf("model server: %s — %s", state, str(h["error"])))
		} else {
			s.status.SetText("model server: " + state)
		}
	case "tie.state":
		st := obj(e["state"])
		switch {
		case !boolean(st["enabled"]):
			s.tie.SetText("")
		case boolean(st["online"]):
			s.tie.SetText("index: " + str(st["collection"]))
		default:
			s.tie.SetText("index: offline")
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
			s.mcp.SetText(fmt.Sprintf("mcp: %d/%d up, %d tools", up, len(servers), tools))
		}
	case "run.started":
		s.status.SetText("running…")
		s.usage.SetText("running…")
	case "run.busy":
		s.status.SetText("a run is already active")
	case "text.delta":
		s.ensureTail()
		s.tailBuf += str(e["text"])
		s.flushTail()
	case "thinking.delta":
		s.status.SetText("thinking…")
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
		if boolean(e["ok"]) {
			s.status.SetText("ready")
			u := obj(e["usage"])
			s.usage.SetText(fmt.Sprintf("in: %d · out: %d · ctx: %d%%",
				int(num(u["tin"])), int(num(u["tout"])), int(num(e["ctx_pct"]))))
		} else {
			s.status.SetText("error")
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
		s.status.SetText("reviewing…")
		s.addNotice("🧠 background review started")
	case "review.skipped":
		s.status.SetText("ready")
		if str(e["reason"]) != "preempted by user" {
			s.addNotice("🧠 review skipped: " + str(e["reason"]))
		}
	case "review.flag":
		s.addNotice("🪧 reviewer flagged a harness gap: " + str(e["gap"]))
		s.refreshActivity()
	case "review.done":
		s.status.SetText("ready")
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
	case "improve.start":
		s.status.SetText("improving harness…")
		s.addNotice("🔧 harness improvement started (run " + str(e["run"]) + ")")
	case "improve.propose":
		s.addNotice("🔧 propose: " + str(e["kind"]) + " editor drafting…")
	case "improve.probe":
		s.addNotice(fmt.Sprintf("🔧 probe %s: %d steps in %ds",
			str(e["kind"]), int(num(e["steps"])), int(num(e["duration_s"]))))
	case "improve.compose":
		s.addNotice("🔧 composed successor")
	case "improve.done":
		s.status.SetText("ready")
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
		s.status.SetText("ready")
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
		s.addNotice("⚙ configuration changed")
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
		s.todos.Add(lbl)
	}
	s.todos.Refresh()
}

func (s *shell) addPerm(e map[string]any) {
	id := str(e["id"])
	info := fmt.Sprintf("🔐 %s — %s\n[%s] %s",
		str(e["tool"]), str(e["message"]), str(e["grant"]), oneLine(str(e["input"])))
	s.addApprovalCard(id, info,
		widget.NewButton("allow", func() { s.a.Runner.RespondPerm(id, "allow") }),
		widget.NewButton("session", func() { s.a.Runner.RespondPerm(id, "session") }),
		widget.NewButton("deny", func() { s.a.Runner.RespondPerm(id, "deny") }),
	)
}

func (s *shell) addApprovalCard(id, info string, buttons ...fyne.CanvasObject) {
	lbl := widget.NewLabel(info)
	lbl.Wrapping = fyne.TextWrapWord
	row := container.NewBorder(nil, nil, nil, container.NewHBox(buttons...), lbl)
	s.permRows[id] = row
	s.perms.Add(row)
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
