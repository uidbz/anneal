package main

import (
	"fmt"
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

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
