package main

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
)

// inspectorPanel is the right-hand column: tasks and (live) context usage.
// It hides sections that have nothing to say rather than showing empty boxes.
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
	sessID    *themedText
	sessMode  *themedText
	harness   *themedText

	// context values arrive with provider/run events
	tin, tout int
	ctxPct    int
	ctxWindow int
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
	ctxSection := container.NewVBox(
		newSectionLabel("CONTEXT"),
		container.NewVBox(p.ctxMeter, p.ctxLabel, p.ctxTokens),
	)

	p.sessID = newThemedText("", colorMuted)
	p.sessMode = newThemedText("", colorMuted)
	p.harness = newThemedText("", colorMuted)
	p.session = container.NewVBox(
		newSectionLabel("SESSION"),
		p.sessID, p.sessMode, p.harness,
	)

	p.object = newSurface(colorSidebar, container.New(&padLayout{l: spaceM, r: spaceM, t: spaceM, b: spaceM},
		container.NewVBox(p.tasksSection, dividerBlock(),
			ctxSection, dividerBlock(), p.session)))
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
		p.ctxLabel.SetText(fmt.Sprintf("%d%% of %s", p.ctxPct, humanK(p.ctxWindow)))
	} else {
		p.ctxLabel.SetText("No run yet")
	}
	if p.tin > 0 || p.tout > 0 {
		p.ctxTokens.SetText(fmt.Sprintf("↑ %s · ↓ %s", humanK(p.tin), humanK(p.tout)))
	} else {
		p.ctxTokens.SetText("")
	}
	p.sessID.SetText(shortSessionID(p.s.sessionID))
	p.sessMode.SetText(modeLabel(p.s.mode))
	p.harness.SetText(harnessLabel(p.s))
	p.object.Refresh()
}

func shortSessionID(id string) string {
	if id == "" {
		return "No session"
	}
	if len(id) > 15 {
		return "Session " + id[:15]
	}
	return "Session " + id
}

func modeLabel(mode string) string {
	if mode == "plan" {
		return "Plan mode"
	}
	return "Build mode"
}

func harnessLabel(s *shell) string {
	return "Harness: base"
}

func dividerBlock() fyne.CanvasObject {
	return container.New(&padLayout{l: 0, r: 0, t: spaceM, b: spaceM}, newDivider())
}
