package schedule

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/events"
)

func utc(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseCron(t *testing.T) {
	loc := time.UTC
	p, err := Parse("0 7 * * 1-5", loc)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindCron {
		t.Fatalf("kind = %q", p.Kind)
	}
	// Friday 2026-11-20 08:00 -> Monday 2026-11-23 07:00.
	after := utc(t, "2026-11-20T08:00:00Z")
	got := p.Next(after)
	want := utc(t, "2026-11-23T07:00:00Z")
	if !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
}

func TestParseIntervalsAndOneShots(t *testing.T) {
	loc := time.UTC
	base := utc(t, "2026-11-22T12:00:00Z")

	p, err := Parse("every 2h", loc)
	if err != nil || p.Kind != KindInterval {
		t.Fatalf("interval: %+v %v", p, err)
	}
	if got := p.Next(base); !got.Equal(base.Add(2 * time.Hour)) {
		t.Fatalf("interval next = %v", got)
	}

	p, err = Parse("1d12h", loc)
	if err != nil || p.Kind != KindOnce {
		t.Fatalf("bare day duration should be one-shot: %+v %v", p, err)
	}
	p, err = Parse("every 1d12h", loc)
	if err != nil || p.Kind != KindInterval || p.every != 36*time.Hour {
		t.Fatalf("day interval: %+v %v", p, err)
	}

	p, err = Parse("2026-12-01T09:30:00", loc)
	if err != nil || p.Kind != KindOnce || !p.Once().Equal(utc(t, "2026-12-01T09:30:00Z")) {
		t.Fatalf("timestamp: %+v %v", p, err)
	}

	p, err = Parse("in 15m", loc)
	if err != nil || p.Kind != KindOnce {
		t.Fatalf("delay: %+v %v", p, err)
	}
	if d := time.Until(p.Once()); d < 14*time.Minute || d > 16*time.Minute {
		t.Fatalf("delay = %v", d)
	}

	if _, err := Parse("not a schedule", loc); err == nil {
		t.Fatal("invalid schedule accepted")
	}
}

func TestDurationDaysWeeks(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"1d", 24 * time.Hour},
		{"1w", 7 * 24 * time.Hour},
		{"1d12h", 36 * time.Hour},
		{"12h1d", 36 * time.Hour},
		{"1.5d", 36 * time.Hour},
		{"90m", 90 * time.Minute},
	} {
		got, err := parseScheduleDuration(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%s = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
}

func TestValidateWorkAndAtom(t *testing.T) {
	for _, ok := range []string{"", "projects/anneal", "a/b"} {
		if err := ValidateWork(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"/etc", "../x", "a/../../b", "a\x00b", `C:\x`} {
		if err := ValidateWork(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if !ValidAtom("app-misc/hello") || !ValidAtom("=sys-devel/gcc-15.2.0") {
		t.Fatal("valid atom rejected")
	}
	if ValidAtom("-rf") || ValidAtom("app-misc/hello;rm -rf /") {
		t.Fatal("invalid atom accepted")
	}
}

func TestStoreRoundTripAndRuns(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(filepath.Join(dir, "schedules"))
	jobs := []Job{{
		ID: "daily", Prompt: "do it", Schedule: "0 7 * * *",
		CreatedAt: utc(t, "2026-11-01T00:00:00Z"),
	}}
	if err := st.Save(jobs); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load()
	if err != nil || len(got) != 1 || got[0].ID != "daily" {
		t.Fatalf("load: %+v %v", got, err)
	}
	rec := RunRecord{ID: "r1", JobID: "daily", Status: StatusOK,
		StartedAt: utc(t, "2026-11-02T07:00:00Z")}
	if err := st.AppendRun(rec); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendRun(RunRecord{ID: "r2", JobID: "daily", Status: StatusFailed,
		StartedAt: utc(t, "2026-11-03T07:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	runs, err := st.Runs(10)
	if err != nil || len(runs) != 2 || runs[0].ID != "r2" {
		t.Fatalf("runs: %+v %v", runs, err)
	}
}

type fakeExecutor struct {
	calls []Job
	rec   RunRecord
	err   error
}

func (f *fakeExecutor) RunJob(ctx context.Context, job Job, rec RunRecord) (RunRecord, error) {
	f.calls = append(f.calls, job)
	out := f.rec
	out.ID = rec.ID
	out.JobID = job.ID
	out.SessionID = "sess-" + job.ID
	out.OK = f.err == nil
	if out.Text == "" {
		out.Text = "done"
	}
	return out, f.err
}

func testManager(t *testing.T, exec Executor, now time.Time) (*Manager, *events.Bus) {
	t.Helper()
	bus := events.New()
	cfg := config.Schedule{Enabled: true, Timezone: "UTC", TickS: 3600, DefaultTimeoutS: 60, GraceS: 60, MaxJobs: 10}
	m, err := New(cfg, NewStore(filepath.Join(t.TempDir(), "schedules")), bus, exec)
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return now }
	return m, bus
}

func TestCreateAndRunDue(t *testing.T) {
	ex := &fakeExecutor{}
	now := utc(t, "2026-11-22T12:00:00Z")
	m, bus := testManager(t, ex, now)
	var seen []string
	bus.AddTap(func(v any) {
		if e, ok := v.(map[string]any); ok {
			if s, ok := e["type"].(string); ok {
				seen = append(seen, s)
			}
		}
	})
	job, err := m.Create(Job{
		ID: "daily", Name: "Daily", Prompt: "summarize", Schedule: "every 1h",
		PermissionMode: "dontAsk",
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.NextRun == nil || !job.NextRun.Equal(now.Add(time.Hour)) {
		t.Fatalf("next run = %v", job.NextRun)
	}
	m.now = func() time.Time { return now.Add(time.Hour) }
	m.runDue(context.Background())
	if len(ex.calls) != 1 {
		t.Fatalf("executor calls = %d", len(ex.calls))
	}
	got, _ := m.Get("daily")
	if got.LastStatus != StatusOK || got.LastSessionID != "sess-daily" || got.RunCount != 1 {
		t.Fatalf("job state: %+v", got)
	}
	if got.NextRun == nil || !got.NextRun.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("next after run = %v", got.NextRun)
	}
	runs, _ := m.Runs(10)
	if len(runs) != 1 || runs[0].Status != StatusOK || runs[0].Text != "done" {
		t.Fatalf("runs: %+v", runs)
	}
	joined := strings.Join(seen, ",")
	if !strings.Contains(joined, "schedule.created") || !strings.Contains(joined, "schedule.started") ||
		!strings.Contains(joined, "schedule.finished") {
		t.Fatalf("events: %s", joined)
	}
}

func TestOneShotDisablesAfterRun(t *testing.T) {
	ex := &fakeExecutor{}
	now := utc(t, "2026-11-22T12:00:00Z")
	m, _ := testManager(t, ex, now)
	job, err := m.Create(Job{ID: "once", Prompt: "do it", Schedule: "2026-11-22T13:00:00"})
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return now.Add(time.Hour) }
	m.runDue(context.Background())
	got, _ := m.Get(job.ID)
	if got.IsEnabled() || got.NextRun != nil {
		t.Fatalf("one-shot still enabled: %+v", got)
	}
	if len(ex.calls) != 1 {
		t.Fatalf("calls = %d", len(ex.calls))
	}
}

func TestManualRunDoesNotAdvanceSchedule(t *testing.T) {
	ex := &fakeExecutor{}
	now := utc(t, "2026-11-22T12:00:00Z")
	m, _ := testManager(t, ex, now)
	job, err := m.Create(Job{ID: "daily", Prompt: "do it", Schedule: "every 6h"})
	if err != nil {
		t.Fatal(err)
	}
	next := *job.NextRun
	if _, err := m.RunNow(job.ID); err != nil {
		t.Fatal(err)
	}
	m.runManual(context.Background(), manualRequest{jobID: job.ID})
	got, _ := m.Get(job.ID)
	if got.NextRun == nil || !got.NextRun.Equal(next) {
		t.Fatalf("manual run changed next: %v -> %v", next, got.NextRun)
	}
	if len(ex.calls) != 1 {
		t.Fatalf("calls = %d", len(ex.calls))
	}
}

func TestReconcileDisablesPastOneShot(t *testing.T) {
	ex := &fakeExecutor{}
	now := utc(t, "2026-11-22T12:00:00Z")
	m, _ := testManager(t, ex, now)
	m.jobs = []Job{{
		ID: "once", Prompt: "do it", Schedule: "2026-11-21T12:00:00", Session: SessionFresh,
		Overlap: OverlapSkip, Missed: MissedSkip, Timezone: "UTC",
		Enabled: boolPtr(true), GraceS: 60, TimeoutS: 60,
	}}
	m.reconcileLocked()
	if m.jobs[0].IsEnabled() || m.jobs[0].NextRun != nil {
		t.Fatalf("past one-shot not disabled: %+v", m.jobs[0])
	}
}

func TestReconcileMissedRuns(t *testing.T) {
	ex := &fakeExecutor{}
	now := utc(t, "2026-11-22T12:00:00Z")
	m, _ := testManager(t, ex, now)
	old := now.Add(-2 * time.Hour)
	m.jobs = []Job{{
		ID: "daily", Prompt: "do it", Schedule: "every 1h", Session: SessionFresh,
		Overlap: OverlapSkip, Missed: MissedSkip, Timezone: "UTC",
		Enabled: boolPtr(true), NextRun: &old, GraceS: 60, TimeoutS: 60,
	}}
	evs := m.reconcileLocked()
	if len(evs) == 0 {
		t.Fatal("expected a missed event")
	}
	if m.jobs[0].NextRun == nil || !m.jobs[0].NextRun.After(now) {
		t.Fatalf("next run not advanced: %v", m.jobs[0].NextRun)
	}
}

func boolPtr(v bool) *bool { return &v }
