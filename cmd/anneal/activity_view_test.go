package main

import (
	"strings"
	"testing"

	"github.com/uidbz/anneal/core/journey"
)

func TestJourneyTextKinds(t *testing.T) {
	cases := []struct {
		e    journey.Entry
		want string
	}{
		{journey.Entry{Kind: "review", Attrs: map[string]any{"lessons": 4}}, "saved 4 lessons"},
		{journey.Entry{Kind: "improve", Attrs: map[string]any{"successor": true, "run": "r1"}}, "applied a successor harness"},
		{journey.Entry{Kind: "improve", Attrs: map[string]any{"successor": false}}, "kept the current harness"},
		{journey.Entry{Kind: "review-flag", Attrs: map[string]any{"gap": "no retry policy"}}, "no retry policy"},
		{journey.Entry{Kind: "harness-promote", Attrs: map[string]any{"entry": "lib-1"}}, "lib-1"},
	}
	for _, c := range cases {
		if got := journeyText(c.e); !strings.Contains(got, c.want) {
			t.Errorf("journeyText(%v) = %q, want %q", c.e.Kind, got, c.want)
		}
	}
}

func TestJourneyTime(t *testing.T) {
	if got := journeyTime("2026-10-30T22:15:01Z"); got != "2026-10-30 22:15" {
		t.Fatalf("journeyTime = %q", got)
	}
	if got := journeyTime(""); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestActivityRefreshesWithRows(t *testing.T) {
	sh := newTestShell(t)
	sh.a.Journey.Append("review", map[string]any{"lessons": 2})
	sh.a.Journey.Append("improve", map[string]any{"successor": true, "run": "r1"})
	sh.refreshActivity()
	if len(sh.activity.Objects) < 3 {
		t.Fatalf("activity rows = %d", len(sh.activity.Objects))
	}
	texts := ""
	for _, o := range sh.activity.Objects {
		if lbl, ok := o.(*themedText); ok {
			texts += lbl.Text() + "\n"
		}
	}
	if !strings.Contains(texts, "JOURNEY") || !strings.Contains(texts, "IMPROVEMENT RUNS") {
		t.Fatalf("section headings missing: %q", texts)
	}
}
