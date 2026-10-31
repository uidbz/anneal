package main

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// queuedItem is one message waiting behind the active run.
type queuedItem struct {
	ID   string
	Text string
	Ref  string
}

// queueTray shows pending messages between the approval dock and the
// composer: the user can see that a message was accepted, that it is queued,
// or that the queue paused after a stop. Without it a send during a run
// looked delivered while the backend had rejected it.
type queueTray struct {
	s       *shell
	surface *surface
	title   *themedText
	clear   *widget.Button
	resume  *widget.Button
	rows    *fyne.Container
	visible bool
}

func newQueueTray(s *shell) *queueTray {
	q := &queueTray{s: s}
	q.title = newThemedText("", colorMuted)
	q.title.size = theme.SizeNameCaptionText
	q.clear = widget.NewButton("Clear", func() { s.b.clearQueue() })
	q.clear.Importance = widget.LowImportance
	q.resume = widget.NewButton("Run next", func() { s.b.resumeQueue() })
	q.resume.Importance = widget.MediumImportance
	q.resume.Hide()
	head := container.NewBorder(nil, nil, q.title,
		container.New(&rowLayout{gap: spaceS}, q.resume, q.clear), nil)
	q.rows = container.New(&columnLayout{gap: 2})
	body := container.New(&columnLayout{gap: spaceXS}, head, q.rows)
	q.surface = newSurface(colorRaised,
		container.New(&padLayout{l: spaceL, r: spaceL, t: spaceS, b: spaceS}, body))
	q.surface.padding(0, 0, 0, 0)
	q.surface.Hide()
	return q
}

func (q *queueTray) Object() fyne.CanvasObject { return q.surface }

// set rebuilds the tray from the shell's queued list.
func (q *queueTray) set(items []queuedItem, paused bool, reason string) {
	q.rows.RemoveAll()
	if len(items) == 0 {
		q.visible = false
		q.surface.Hide()
		q.s.relayoutChat()
		return
	}
	q.visible = true
	if paused {
		r := reason
		if r == "" {
			r = "previous run stopped"
		}
		q.title.SetText(fmt.Sprintf("Queue paused — %s", r))
		q.resume.Show()
	} else {
		q.title.SetText(fmt.Sprintf("Queued (%d) — runs after the current task", len(items)))
		q.resume.Hide()
	}
	for i, item := range items {
		item := item
		label := newThemedText(fmt.Sprintf("%d. %s", i+1, oneLine(item.Text)), theme.ColorNameForeground)
		next := widget.NewButton("Run next", func() { q.s.b.promoteQueued(item.ID) })
		next.Importance = widget.LowImportance
		edit := widget.NewButton("Edit", func() {
			q.s.entry.SetText(item.Text)
			q.s.b.cancelQueued(item.ID)
			q.s.entry.Refresh()
		})
		edit.Importance = widget.LowImportance
		del := iconButton(theme.CancelIcon(), func() { q.s.b.cancelQueued(item.ID) })
		row := container.NewBorder(nil, nil, nil,
			container.New(&rowLayout{gap: spaceXS}, next, edit, del), label)
		q.rows.Add(row)
	}
	q.surface.Show()
	q.surface.Refresh()
	q.s.relayoutChat()
}

// queuedIndex returns the index of id in the shell's queue, or -1.
func (s *shell) queuedIndex(id string) int {
	for i, q := range s.queued {
		if q.ID == id {
			return i
		}
	}
	return -1
}

// setQueuedFooters updates the small status line under a user turn. Messages
// from other clients have no footer until they are first seen.
func (s *shell) setQueuedFooters(ref, text string) {
	if ref == "" || s.userFooters == nil {
		return
	}
	if footer := s.userFooters[ref]; footer != nil {
		footer.SetText(text)
	}
}
