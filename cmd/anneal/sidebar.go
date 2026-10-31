package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/uidbz/anneal/core/agent"
)

// sessionsSidebar lists stored sessions grouped by age, filterable, with the
// current session highlighted. It is always visible in the chat view (unless
// collapsed), replacing the old sessions dialog.
type sessionsSidebar struct {
	s      *shell
	object fyne.CanvasObject
	filter *widget.Entry

	list  *widget.List
	count *badge
	empty *themedText
	rows  []sessionRow
	infos []agent.SessionInfo
	now   func() time.Time // injectable clock for tests
	load  func() []agent.SessionInfo
}

// sessionRow is either a group heading or a session entry in the flat list.
type sessionRow struct {
	heading string
	info    agent.SessionInfo
	group   string
}

func newSessionsSidebar(s *shell) *sessionsSidebar {
	sb := &sessionsSidebar{s: s, now: time.Now,
		load: func() []agent.SessionInfo { return s.b.sessions() }}

	sb.filter = widget.NewEntry()
	sb.filter.PlaceHolder = "Filter sessions"
	sb.filter.OnChanged = func(string) { sb.refresh() }

	newBtn := iconButton(theme.ContentAddIcon(), func() { s.b.newSession() })

	sb.count = newBadge("", colorRaised, colorMuted)
	head := container.NewBorder(nil, nil,
		container.New(&rowLayout{gap: spaceS}, newSectionLabel("SESSIONS"), sb.count), newBtn, nil)

	sb.list = widget.NewList(
		func() int { return len(sb.rows) },
		func() fyne.CanvasObject { return newSessionRowItem(sb) },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i >= len(sb.rows) {
				return
			}
			o.(*sessionRowItem).set(sb.rows[i])
		},
	)
	sb.list.HideSeparators = true
	sb.list.OnSelected = func(id widget.ListItemID) {
		defer sb.list.UnselectAll()
		if id < 0 || id >= len(sb.rows) {
			return
		}
		row := sb.rows[id]
		if row.heading != "" || row.info.ID == "" {
			return
		}
		s.b.openSession(row.info.ID)
	}

	sb.empty = newThemedText("No sessions yet", colorMuted)
	center := container.NewStack(sb.list, container.NewCenter(sb.empty))
	body := container.NewBorder(
		container.New(&padLayout{l: spaceM, r: spaceS, t: spaceM, b: spaceS},
			container.NewVBox(head, sb.filter)),
		nil, nil, nil, center)
	sb.object = newSurface(colorSidebar, body)
	sb.refresh()
	return sb
}

func (sb *sessionsSidebar) Object() fyne.CanvasObject { return sb.object }

// refresh reloads the session list, applies the filter, and rebuilds the
// grouped rows.
func (sb *sessionsSidebar) refresh() {
	sb.infos = sb.load()
	query := strings.ToLower(strings.TrimSpace(sb.filter.Text))
	now := sb.now()
	rows := groupSessions(sb.infos, now)
	if query != "" {
		filtered := rows[:0]
		for _, r := range rows {
			if r.heading != "" {
				continue
			}
			title := strings.ToLower(sessionTitle(r.info))
			if strings.Contains(title, query) || strings.Contains(strings.ToLower(r.info.ID), query) {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}
	sb.rows = rows
	sb.count.SetText(fmt.Sprintf("%d", len(sb.infos)))
	sb.empty.Hide()
	if len(rows) == 0 {
		sb.empty.Show()
	}
	sb.list.Refresh()
	for i, r := range rows {
		h := theme.Size(theme.SizeNameText)*2 + spaceS
		if r.heading != "" {
			h = theme.Size(theme.SizeNameCaptionText) + spaceS
		}
		sb.list.SetItemHeight(i, h)
	}
}

// groupSessions sorts newest first and inserts group headings.
func groupSessions(infos []agent.SessionInfo, now time.Time) []sessionRow {
	sorted := append([]agent.SessionInfo{}, infos...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Updated > sorted[j].Updated
	})
	var rows []sessionRow
	lastGroup := ""
	for _, info := range sorted {
		group := sessionGroup(info.Updated, now)
		if group != lastGroup {
			rows = append(rows, sessionRow{heading: group})
			lastGroup = group
		}
		rows = append(rows, sessionRow{info: info, group: group})
	}
	return rows
}

// sessionGroup buckets a "2006-01-02 15:04" timestamp into a heading.
func sessionGroup(updated string, now time.Time) string {
	t, err := time.ParseInLocation("2006-01-02 15:04", updated, time.Local)
	if err != nil {
		return "Older"
	}
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
	switch {
	case day.Equal(today):
		return "Today"
	case day.Equal(today.AddDate(0, 0, -1)):
		return "Yesterday"
	case day.After(today.AddDate(0, 0, -7)):
		return "Previous 7 days"
	default:
		return "Older"
	}
}

// relativeTime renders a timestamp compactly for the session list.
func relativeTime(updated string, now time.Time) string {
	t, err := time.ParseInLocation("2006-01-02 15:04", updated, time.Local)
	if err != nil {
		return updated
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case now.Sub(t) < 24*time.Hour && now.Day() == t.Day():
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case now.AddDate(0, 0, -6).Before(t):
		return t.Format("Mon 15:04")
	default:
		return t.Format("Jan 2")
	}
}

func sessionTitle(info agent.SessionInfo) string {
	if strings.TrimSpace(info.Title) == "" {
		return "Untitled session"
	}
	return info.Title
}

// sessionRowItem is one reusable list row: a group heading or a two-line
// session entry with an accent bar when it is the current session.
type sessionRowItem struct {
	widget.BaseWidget
	sb      *sessionsSidebar
	bar     *themedRect
	title   *themedText
	meta    *themedText
	content fyne.CanvasObject
	row     sessionRow
}

func newSessionRowItem(sb *sessionsSidebar) *sessionRowItem {
	it := &sessionRowItem{sb: sb}
	it.bar = newThemedRect(colorSidebar, 3, 0)
	it.title = newThemedText("", theme.ColorNameForeground)
	it.meta = newThemedText("", colorMuted)
	it.content = container.NewBorder(nil, nil,
		container.New(&padLayout{l: 0, r: spaceS, t: 0, b: 0}, it.bar), nil,
		container.NewVBox(it.title, it.meta))
	it.ExtendBaseWidget(it)
	return it
}

func (it *sessionRowItem) set(row sessionRow) {
	it.row = row
	if row.heading != "" {
		it.title.SetText(strings.ToUpper(row.heading))
		it.title.size = theme.SizeNameCaptionText
		it.title.bold = true
		it.title.token = colorMuted
		it.meta.SetText("")
		it.bar.setToken(colorSidebar)
		it.title.Refresh()
		it.meta.Hide()
		return
	}
	it.title.size = theme.SizeNameText
	it.title.bold = false
	current := row.info.ID != "" && row.info.ID == it.sb.s.sessionID
	if current {
		it.title.token = theme.ColorNamePrimary
		it.bar.setToken(theme.ColorNamePrimary)
	} else {
		it.title.token = theme.ColorNameForeground
		it.bar.setToken(colorSidebar)
	}
	it.title.SetText(sessionTitle(row.info))
	it.meta.SetText(relativeTime(row.info.Updated, it.sb.now()))
	it.meta.Show()
	it.title.Refresh()
}

// SecondaryTapped offers the session ID on right-click.
func (it *sessionRowItem) SecondaryTapped(ev *fyne.PointEvent) {
	if it.row.heading != "" || it.row.info.ID == "" {
		return
	}
	id := it.row.info.ID
	menu := fyne.NewMenu("",
		fyne.NewMenuItem("Copy session ID", func() {
			it.sb.s.win.Clipboard().SetContent(id)
			it.sb.s.addNotice("✔ copied session ID")
		}),
	)
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(it)
	widget.ShowPopUpMenuAtPosition(menu, it.sb.s.win.Canvas(),
		fyne.NewPos(pos.X+ev.Position.X, pos.Y+ev.Position.Y))
}

func (it *sessionRowItem) Tapped(*fyne.PointEvent) {
	if it.row.heading != "" || it.row.info.ID == "" {
		return
	}
	it.sb.s.b.openSession(it.row.info.ID)
}

func (it *sessionRowItem) CreateRenderer() fyne.WidgetRenderer {
	return &sessionRowRenderer{item: it, pad: container.New(&padLayout{l: spaceM, r: spaceM, t: 2, b: 2}, it.content)}
}

type sessionRowRenderer struct {
	item *sessionRowItem
	pad  *fyne.Container
}

func (r *sessionRowRenderer) Layout(size fyne.Size) {
	r.pad.Move(fyne.NewPos(0, 0))
	r.pad.Resize(size)
}

func (r *sessionRowRenderer) MinSize() fyne.Size {
	return r.pad.MinSize()
}

func (r *sessionRowRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.pad} }

func (r *sessionRowRenderer) Refresh() { r.pad.Refresh() }

func (r *sessionRowRenderer) Destroy() {}

// newSectionLabel is the small uppercase caption used above panel sections.
func newSectionLabel(text string) *themedText {
	t := newThemedText(text, colorMuted)
	t.size = theme.SizeNameCaptionText
	t.bold = true
	return t
}
