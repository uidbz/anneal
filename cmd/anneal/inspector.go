package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/uidbz/anneal/core/paths"
)

// inspectorPanel is the right-hand column: tasks, context usage, and session
// facts. Sections collapse themselves when they have nothing to say.
type inspectorPanel struct {
	s      *shell
	object fyne.CanvasObject

	tasks        *fyne.Container // the task rows (s.todos)
	tasksHead    *fyne.Container
	tasksSection *fyne.Container
	context      *fyne.Container
	session      *fyne.Container

	taskCount *badge
	ctxMeter  *meter
	ctxLabel  *themedText
	ctxTokens *themedText
	ctxComp   *themedText
	sessID    *themedText
	sessCwd   *themedText
	sessMode  *themedText
	harness   *themedText

	// context values arrive with provider/run events
	tin, tout int
	ctxPct    int
	compacted int
}

func newInspectorPanel(s *shell) *inspectorPanel {
	p := &inspectorPanel{s: s, taskCount: newBadge("", colorRaised, colorMuted)}

	p.tasks = s.todos
	p.tasksHead = container.NewBorder(nil, nil,
		container.New(&rowLayout{gap: spaceS}, newSectionLabel("TASKS"), p.taskCount), nil, nil)
	p.tasksSection = container.NewVBox(p.tasksHead, p.tasks)

	p.ctxMeter = newMeter(0, 6)
	p.ctxLabel = newThemedText("No run yet", colorMuted)
	p.ctxTokens = newThemedText("", colorMuted)
	p.ctxComp = newThemedText("", colorMuted)
	ctxSection := container.NewVBox(
		newSectionLabel("CONTEXT"),
		container.New(&padLayout{l: 0, r: 0, t: spaceXS, b: spaceXS}, p.ctxMeter),
		p.ctxLabel, p.ctxTokens, p.ctxComp,
	)

	p.sessID = newThemedText("", colorMuted)
	p.sessCwd = newThemedText("", colorMuted)
	p.sessMode = newThemedText("", colorMuted)
	p.harness = newThemedText("", colorMuted)
	copyBtn := iconButton(theme.ContentCopyIcon(), func() {
		if s.sessionID == "" {
			return
		}
		s.win.Clipboard().SetContent(s.sessionID)
		s.notice(noticeSuccess, "Session ID copied.")
	})
	copyBtn.Importance = widget.LowImportance
	idRow := container.NewBorder(nil, nil, nil, copyBtn, p.sessID)

	cwdCopy := iconButton(theme.ContentCopyIcon(), func() {
		s.win.Clipboard().SetContent(s.a.Runner.Cwd())
		s.notice(noticeSuccess, "Working directory copied.")
	})
	cwdCopy.Importance = widget.LowImportance
	cwdRow := container.NewBorder(nil, nil,
		container.New(&padLayout{l: 0, r: spaceS, t: 0, b: 0}, newSectionLabel("CWD")), cwdCopy, p.sessCwd)
	p.session = container.NewVBox(
		newSectionLabel("SESSION"), idRow, cwdRow, p.sessMode, p.harness,
	)

	body := container.New(&columnLayout{gap: 0}, p.tasksSection, dividerBlock(),
		ctxSection, dividerBlock(), p.session)
	p.object = newSurface(colorSidebar, container.New(&padLayout{l: spaceM, r: spaceM, t: spaceM, b: spaceM},
		container.NewVScroll(body)))
	s.todoPane = p.tasksSection
	p.refresh()
	return p
}

func (p *inspectorPanel) Object() fyne.CanvasObject { return p.object }

// refresh updates the derived sections (tasks are rendered by renderTodos).
func (p *inspectorPanel) refresh() {
	p.taskCount.SetText(fmt.Sprintf("%d", len(p.s.todos.Objects)))
	p.ctxMeter.SetValue(float64(p.ctxPct) / 100)
	if p.ctxPct > 0 {
		p.ctxLabel.SetText(fmt.Sprintf("%d%% of %s tokens", p.ctxPct, humanK(p.s.ctxWindow)))
	} else {
		p.ctxLabel.SetText("No run yet")
	}
	if p.tin > 0 || p.tout > 0 {
		p.ctxTokens.SetText(fmt.Sprintf("↑ %s in · ↓ %s out", humanK(p.tin), humanK(p.tout)))
	} else {
		p.ctxTokens.SetText("")
	}
	if p.compacted > 0 {
		p.ctxComp.SetText(fmt.Sprintf("%d compaction(s) this session", p.compacted))
	} else {
		p.ctxComp.SetText("")
	}
	p.sessID.SetText(shortSessionID(p.s.sessionID))
	p.sessCwd.SetText(shortPath(p.s.a.Runner.Cwd(), 34))
	p.sessMode.SetText(modeLabel(p.s.mode))
	p.harness.SetText(harnessLabel(p.s))
	p.object.Refresh()
}

func shortSessionID(id string) string {
	if id == "" {
		return "No session"
	}
	if len(id) > 18 {
		return id[:18] + "…"
	}
	return id
}

// shortPath keeps the tail of a path readable in the narrow inspector.
func shortPath(path string, max int) string {
	if len(path) <= max {
		return path
	}
	tail := path[len(path)-max+1:]
	if i := strings.IndexByte(tail, '/'); i >= 0 {
		tail = tail[i+1:]
	}
	return "…/" + tail
}

func modeLabel(mode string) string {
	if mode == "plan" {
		return "Plan mode"
	}
	return "Build mode"
}

// harnessLabel reports whether this session runs the base harness or an
// improvement successor overlay.
func harnessLabel(s *shell) string {
	if s.sessionID == "" {
		return "Harness: base"
	}
	dir := filepath.Join(paths.Overlays(), s.sessionID)
	if _, err := os.Stat(dir); err == nil {
		return "Harness: improvement overlay"
	}
	return "Harness: base"
}

func dividerBlock() fyne.CanvasObject {
	return container.New(&padLayout{l: 0, r: 0, t: spaceM, b: spaceM}, newDivider())
}
