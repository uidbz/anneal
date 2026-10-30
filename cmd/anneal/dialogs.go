package main

import (
	"encoding/json"
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

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
