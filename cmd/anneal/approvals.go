package main

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// pendingApproval is one card waiting for a decision; the shell keeps them in
// arrival order so Ctrl+Enter can allow the oldest.
type pendingApproval struct {
	id      string
	kind    string // permission | memory | skill
	allow   func()
	always  func() // nil for memory/skill cards (allow is the only approval)
	reject  func()
	card    fyne.CanvasObject
	created time.Time
	age     *themedText
}

// addPermCard renders a permission request.
func (s *shell) addPerm(e map[string]any) {
	id := str(e["id"])
	action := str(e["action"])
	if action == "" {
		action = str(e["tool"])
	}
	resources := oneLine(strings.Join(stringList(e["resources"]), ", "))
	input := oneLine(str(e["input"]))
	detail := ""
	if resources != "" {
		detail += "resource  " + resources + "\n"
	}
	if input != "" {
		detail += "input     " + input
	}
	grant := str(e["grant"])
	title := "Permission required"
	if action != "" {
		title += " · " + action
	}
	s.addApprovalCard(pendingApproval{
		id:     id,
		kind:   "permission",
		allow:  func() { s.b.respondPerm(id, "allow") },
		always: func() { s.b.respondPerm(id, "session") },
		reject: func() { s.b.respondPerm(id, "deny") },
	}, title, str(e["message"]), detail, grant,
		approvalButton("Reject", widget.LowImportance, func() { s.b.respondPerm(id, "deny") }),
		approvalButton("Always allow", widget.MediumImportance, func() { s.b.respondPerm(id, "session") }),
		approvalButton("Allow once", widget.HighImportance, func() { s.b.respondPerm(id, "allow") }),
	)
}

// addMemoryCard renders a staged memory change.
func (s *shell) addMemoryCard(e map[string]any) {
	op := obj(e["op"])
	id := str(op["id"])
	title := fmt.Sprintf("Memory change · %s %s", str(op["target"]), str(op["action"]))
	s.addApprovalCard(pendingApproval{
		id:     id,
		kind:   "memory",
		allow:  func() { s.b.respondMemory(id, "approve") },
		reject: func() { s.b.respondMemory(id, "reject") },
	}, title, "Proposed by "+str(op["source"]), memoryOpText(op), "",
		approvalButton("Reject", widget.LowImportance, func() { s.b.respondMemory(id, "reject") }),
		approvalButton("Approve", widget.HighImportance, func() { s.b.respondMemory(id, "approve") }),
	)
}

// addSkillCard renders a staged skill change.
func (s *shell) addSkillCard(e map[string]any) {
	op := obj(e["op"])
	id := str(op["id"])
	title := fmt.Sprintf("Skill change · %s %s", str(op["action"]), str(op["name"]))
	s.addApprovalCard(pendingApproval{
		id:     id,
		kind:   "skill",
		allow:  func() { s.b.respondSkill(id, "approve") },
		reject: func() { s.b.respondSkill(id, "reject") },
	}, title, "Proposed by "+str(op["source"]), oneLine(str(e["preview"])), "",
		approvalButton("Reject", widget.LowImportance, func() { s.b.respondSkill(id, "reject") }),
		approvalButton("Approve", widget.HighImportance, func() { s.b.respondSkill(id, "approve") }),
	)
}

func approvalButton(label string, importance widget.Importance, fn func()) *widget.Button {
	b := widget.NewButton(label, fn)
	b.Importance = importance
	return b
}

// addApprovalCard builds the warn-washed card and docks it above the composer.
func (s *shell) addApprovalCard(p pendingApproval, title, message, detail, grant string,
	buttons ...fyne.CanvasObject) {

	if p.id != "" {
		if _, exists := s.permRows[p.id]; exists {
			return // replay after reconnect: the card is already docked
		}
	}
	p.created = time.Now()
	p.age = newThemedText("waiting 0s", colorMuted)
	p.age.size = theme.SizeNameCaptionText

	titleLbl := newThemedText(title, colorWarn)
	titleLbl.bold = true
	titleRow := container.NewBorder(nil, nil, nil,
		container.New(&padLayout{l: spaceS, r: 0, t: 0, b: 0}, p.age), titleLbl)
	msg := selectableLabel(message, false)
	parts := []fyne.CanvasObject{titleRow, msg}
	if strings.TrimSpace(detail) != "" {
		detailLbl := selectableLabel(detail, true)
		detailView := newSurface(colorCode,
			container.New(&padLayout{l: spaceS, r: spaceS, t: 2, b: 2}, detailLbl)).
			border(colorBorder, 1)
		// Long detail stays inside a fixed-height scroll so the action row
		// below it is never pushed out of the dock.
		scroll := container.NewVScroll(detailView)
		scroll.SetMinSize(fyne.NewSize(0, 6*theme.Size(theme.SizeNameText)))
		parts = append(parts, scroll)
	}
	if grant != "" {
		parts = append(parts, newThemedText("Always allows: "+grant, colorMuted))
	}
	actionRow := container.NewBorder(nil, nil, nil, container.New(&rowLayout{gap: spaceS}, buttons...))
	parts = append(parts, actionRow)
	body := container.NewVBox(parts...)

	panel := newSurface(colorWarnWash,
		container.New(&padLayout{l: spaceM, r: spaceM, t: spaceS, b: spaceS}, body)).
		border(colorWarn, 1)
	bar := newThemedRect(colorWarn, 3, 0)
	cardObj := container.NewBorder(nil, nil, container.New(&padLayout{l: 0, r: 0, t: 0, b: 0}, bar), nil, panel)

	p.card = cardObj
	s.permRows[p.id] = cardObj
	s.pendingApprovals = append(s.pendingApprovals, p)
	s.dock.add(cardObj)
	s.updateApprovalStatus()
}

// updateApprovalAges refreshes the "waiting" counters while cards are pending.
func (s *shell) updateApprovalAges() {
	for _, p := range s.pendingApprovals {
		if p.age == nil {
			continue
		}
		p.age.SetText("waiting " + formatElapsed(time.Since(p.created).Round(time.Second)))
	}
}

// removeCard removes a resolved approval from the dock.
func (s *shell) removeCard(id string) {
	row, ok := s.permRows[id]
	if !ok {
		return
	}
	delete(s.permRows, id)
	for i, p := range s.pendingApprovals {
		if p.id == id {
			s.pendingApprovals = append(s.pendingApprovals[:i], s.pendingApprovals[i+1:]...)
			break
		}
	}
	s.dock.remove(row)
	s.updateApprovalStatus()
}

// allowOldest approves the oldest pending card (Ctrl+Enter in the composer).
func (s *shell) allowOldest() {
	if len(s.pendingApprovals) == 0 {
		return
	}
	s.pendingApprovals[0].allow()
}

// alwaysAllowOldest approves the oldest card and saves its grant.
func (s *shell) alwaysAllowOldest() {
	if len(s.pendingApprovals) == 0 {
		return
	}
	if always := s.pendingApprovals[0].always; always != nil {
		always()
		return
	}
	s.pendingApprovals[0].allow()
}

// rejectOldest is the keyboard counterpart of allowOldest.
func (s *shell) rejectOldest() {
	if len(s.pendingApprovals) == 0 {
		return
	}
	s.pendingApprovals[0].reject()
}

// addPermEvent routes a permission request into the dock.
func (s *shell) addPermEvent(e map[string]any) { s.addPerm(e) }
