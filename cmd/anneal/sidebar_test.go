package main

import (
	"fmt"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/uidbz/anneal/core/agent"
)

func TestSessionGrouping(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	infos := []agent.SessionInfo{
		{ID: "a", Title: "old", Updated: "2026-10-01 09:00"},
		{ID: "b", Title: "today", Updated: "2026-10-30 10:00"},
		{ID: "c", Title: "yesterday", Updated: "2026-10-29 18:00"},
		{ID: "d", Title: "recent", Updated: "2026-10-27 08:00"},
	}
	rows := groupSessions(infos, now)
	var got []string
	for _, r := range rows {
		if r.heading != "" {
			got = append(got, "##"+r.heading)
		} else {
			got = append(got, r.info.ID)
		}
	}
	want := []string{"##Today", "b", "##Yesterday", "c", "##Previous 7 days", "d", "##Older", "a"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	cases := map[string]string{
		"2026-10-30 11:59": "1m ago",
		"2026-10-30 09:00": "3h ago",
		"2026-10-28 09:00": "Wed 09:00",
		"2026-09-01 09:00": "Sep 1",
	}
	for in, want := range cases {
		if got := relativeTime(in, now); got != want {
			t.Errorf("relativeTime(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSidebarFilterAndCurrent(t *testing.T) {
	sh := newTestShell(t)
	now := time.Now()
	sh.side.load = func() []agent.SessionInfo {
		return []agent.SessionInfo{
			{ID: "s1", Title: "Fix the parser", Updated: now.Format("2006-01-02 15:04")},
			{ID: "s2", Title: "Write docs", Updated: now.Format("2006-01-02 15:04")},
		}
	}
	sh.side.now = func() time.Time { return now }
	sh.sessionID = "s2"
	sh.side.refresh()

	if len(sh.side.rows) != 3 { // heading + two sessions
		t.Fatalf("rows = %d (%v)", len(sh.side.rows), sh.side.rows)
	}
	sh.side.filter.SetText("parser")
	if len(sh.side.rows) != 1 || sh.side.rows[0].info.ID != "s1" {
		t.Fatalf("filtered rows = %+v", sh.side.rows)
	}
	sh.side.filter.SetText("")
	sh.side.refresh()

	// Current session marker: primary title, accent bar.
	item := newSessionRowItem(sh.side)
	for _, r := range sh.side.rows {
		if r.info.ID == "s2" {
			item.set(r)
		}
	}
	if item.title.token != theme.ColorNamePrimary {
		t.Fatalf("current session title token = %v", item.title.token)
	}
	item.set(sh.side.rows[1])
	if item.title.token == theme.ColorNamePrimary {
		t.Fatal("non-current session highlighted")
	}
}

func TestSidebarRefreshesOnSessionEvents(t *testing.T) {
	sh := newTestShell(t)
	calls := 0
	sh.side.load = func() []agent.SessionInfo {
		calls++
		return nil
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "session.opened", "id": "x", "title": "t", "blocks": []any{}}))
	if calls == 0 {
		t.Fatal("sidebar did not reload on session.opened")
	}
	before := calls
	sh.handleEvent(normalizeEvent(map[string]any{"type": "session.started", "id": "y", "title": ""}))
	if calls <= before {
		t.Fatal("sidebar did not reload on session.started")
	}
	if sh.side.empty.Visible() != true {
		t.Fatal("empty state not shown for an empty session list")
	}
}

func TestSessionsSidebarObject(t *testing.T) {
	sh := newTestShell(t)
	if sh.side.Object() == nil {
		t.Fatal("sidebar has no object")
	}
	if sh.side.Object().MinSize().Width == 0 {
		t.Fatal("sidebar has no minimum width")
	}
	_ = fyne.NewSize(0, 0)
}
