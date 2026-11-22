package main

import (
	"strings"
	"testing"
	"time"

	"github.com/uidbz/anneal/core/schedule"
)

func TestSchedulePanelCardsAndLabels(t *testing.T) {
	sh := newTestShell(t)
	p := sh.schedule
	if p == nil {
		t.Fatal("schedule panel not built")
	}
	next := time.Now().Add(time.Hour)
	job := schedule.Job{
		ID: "daily", Name: "Daily brief", Prompt: "summarize the world",
		Schedule: "0 7 * * *", Enabled: boolPointer(true), NextRun: &next,
		LastStatus: schedule.StatusOK,
	}
	p.setJobs([]schedule.Job{job})
	if len(p.body.Objects) != 1 {
		t.Fatalf("cards = %d", len(p.body.Objects))
	}
	if !strings.Contains(p.status.Text(), "1 job") {
		t.Fatalf("status = %q", p.status.Text())
	}
	if got := jobLabel(job); got != "Daily brief (daily)" {
		t.Fatalf("label = %q", got)
	}
	if !strings.Contains(p.status.Text(), "management-only") && !sh.b.remote() {
		t.Fatalf("local status should mention management-only: %q", p.status.Text())
	}

	p.setJobs(nil)
	if len(p.body.Objects) != 1 || !strings.Contains(p.status.Text(), "No scheduled jobs") {
		t.Fatalf("empty state: %d %q", len(p.body.Objects), p.status.Text())
	}
}

func TestScheduleHelpers(t *testing.T) {
	if got := splitList(" ntfy, matrix-home ,, "); len(got) != 2 || got[0] != "ntfy" || got[1] != "matrix-home" {
		t.Fatalf("splitList = %v", got)
	}
	if got := atoiDefault("", 7); got != 7 {
		t.Fatalf("atoi default = %d", got)
	}
	if got := atoiDefault("42", 7); got != 42 {
		t.Fatalf("atoi = %d", got)
	}
}

func boolPointer(v bool) *bool { return &v }
