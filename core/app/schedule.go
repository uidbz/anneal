package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/uidbz/anneal/core/agent"
	"github.com/uidbz/anneal/core/events"
	"github.com/uidbz/anneal/core/paths"
	"github.com/uidbz/anneal/core/schedule"
)

// StartSchedules starts the durable scheduler. Only the long-running backend
// (anneald) calls this; the GUI constructs the manager for management but
// does not execute jobs. The single-owner file lock makes a second anneald
// fail closed.
func (a *App) StartSchedules(ctx context.Context) error {
	if a.Schedule == nil || !a.Schedule.Enabled() {
		return nil
	}
	return a.Schedule.Start(ctx)
}

// RunJob executes one scheduled job in an isolated worker Runner. The worker
// has a private event bus: the foreground transcript never fills with job
// tool output. The manager emits schedule.* summaries on the main bus.
func (a *App) RunJob(ctx context.Context, job schedule.Job, rec schedule.RunRecord) (schedule.RunRecord, error) {
	root := a.Runner.Cwd()
	work := root
	if job.Work != "" {
		clean := schedule.NormalizeWork(job.Work)
		if err := schedule.ValidateWork(clean); err != nil {
			return rec, err
		}
		work = filepath.Join(root, filepath.FromSlash(clean))
		if work != root && !strings.HasPrefix(work, root+string(filepath.Separator)) {
			return rec, fmt.Errorf("work %q escapes the work root", job.Work)
		}
		if err := os.MkdirAll(work, 0o755); err != nil {
			return rec, err
		}
	}

	cfg := *a.Cfg
	cfg.Agent.PermissionMode = job.PermissionMode
	cfg.Agent.Permission = job.Permission
	cfg.Agent.Permissions = nil
	bus := events.New()
	worker, err := agent.NewRunner(&cfg, a.Monitor, bus, work, a.Harness)
	if err != nil {
		return rec, err
	}
	defer worker.Close()
	defer a.resumeForegroundQueue()
	worker.SetRunGate(a.RunGate)
	worker.SetIndex(a.Tie)
	worker.SetMemory(a.Mem)
	worker.SetSkills(a.Skills)
	worker.SetMCP(a.MCP)
	worker.SetReview(nil) // scheduled jobs must not enqueue themselves for review
	worker.SetPreempt(a.PreemptBackground)
	worker.ConfigurePermissions(job.PermissionMode, job.Permission)

	if job.Session == schedule.SessionSticky && job.SessionID != "" {
		worker.OpenSession(job.SessionID)
		if worker.SessionID() != job.SessionID {
			worker.NewSession()
		}
	} else {
		worker.NewSession()
	}
	worker.ConfigureSession(work, "schedule", job.ID)

	finished := make(chan schedule.RunRecord, 1)
	bus.AddTap(func(v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		switch m["type"] {
		case "run.finished":
			select {
			case finished <- runRecordFromEvent(rec, m):
			default:
			}
		case "perm.request":
			a.forwardSchedulePerm(job, m)
		case "perm.resolved", "perm.saved":
			a.forwardScheduleEvent(job, m)
		}
	})

	a.schedMu.Lock()
	a.schedRunner = worker
	a.schedPermIDs = map[string]string{}
	a.schedMu.Unlock()
	defer func() {
		a.schedMu.Lock()
		a.schedRunner = nil
		a.schedPermIDs = nil
		a.schedMu.Unlock()
	}()

	var runID int
	for {
		id, ok := worker.StartTaskWork(job.Prompt, work)
		if ok {
			runID = id
			break
		}
		select {
		case <-ctx.Done():
			return rec, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}

	select {
	case out := <-finished:
		out.SessionID = worker.SessionID()
		out.Run = runID
		return out, nil
	case <-ctx.Done():
		worker.Interrupt() // soft: finish the current tool, start no new one
		select {
		case out := <-finished:
			out.SessionID = worker.SessionID()
			out.Run = runID
			return out, ctx.Err()
		case <-time.After(10 * time.Second):
		}
		worker.Interrupt() // hard
		select {
		case out := <-finished:
			out.SessionID = worker.SessionID()
			out.Run = runID
			return out, ctx.Err()
		case <-time.After(10 * time.Second):
			return rec, fmt.Errorf("scheduled run did not stop after interrupt")
		}
	}
}

// forwardSchedulePerm rebroadcasts a worker approval card with a namespaced id
// and job context. The gateway can show it on a phone; RespondPerm routes the
// answer back to the active worker.
func (a *App) forwardSchedulePerm(job schedule.Job, m map[string]any) {
	local, _ := m["id"].(string)
	if local == "" {
		return
	}
	forward := "sched-" + job.ID + "-" + local
	a.schedMu.Lock()
	if a.schedPermIDs != nil {
		a.schedPermIDs[forward] = local
	}
	a.schedMu.Unlock()
	out := cloneEvent(m)
	out["id"] = forward
	out["origin"] = "schedule"
	out["job_id"] = job.ID
	out["job_name"] = job.Name
	a.Bus.Emit(out)
}

func (a *App) forwardScheduleEvent(job schedule.Job, m map[string]any) {
	out := cloneEvent(m)
	if id, _ := m["id"].(string); id != "" {
		out["id"] = "sched-" + job.ID + "-" + id
	}
	out["origin"] = "schedule"
	out["job_id"] = job.ID
	out["job_name"] = job.Name
	a.Bus.Emit(out)
}

// RespondPerm answers a foreground or scheduled-job approval card. Schedule
// ids are prefixed and routed to the active worker.
func (a *App) RespondPerm(id, action string) bool {
	a.schedMu.Lock()
	local, scheduled := a.schedPermIDs[id]
	worker := a.schedRunner
	a.schedMu.Unlock()
	if scheduled && worker != nil {
		worker.RespondPerm(local, action)
		return true
	}
	a.Runner.RespondPerm(id, action)
	return false
}

// ScheduleBusy reports a running scheduled job (the server uses it for idle
// shutdown and queue decisions).
func (a *App) ScheduleBusy() bool {
	return a.Schedule != nil && a.Schedule.Busy()
}

// resumeForegroundQueue drains messages queued while a scheduled job held the
// shared run gate. A queue paused by a stop/error stays paused.
func (a *App) resumeForegroundQueue() {
	items, paused, _ := a.Runner.Queue()
	if paused || len(items) == 0 {
		return
	}
	a.Runner.ResumeQueue()
}

// ScheduleNextRun reports the next enabled job for the idle timer.
func (a *App) ScheduleNextRun() (time.Time, string, bool) {
	if a.Schedule == nil {
		return time.Time{}, "", false
	}
	return a.Schedule.NextRun()
}

func runRecordFromEvent(base schedule.RunRecord, m map[string]any) schedule.RunRecord {
	out := base
	out.OK, _ = m["ok"].(bool)
	out.Stopped, _ = m["stopped"].(bool)
	out.Text, _ = m["text"].(string)
	out.Error, _ = m["error"].(string)
	if u, ok := m["usage"].(map[string]any); ok {
		out.Usage = u
	}
	if n, ok := intValue(m["run"]); ok {
		out.Run = n
	}
	switch {
	case out.OK:
		out.Status = schedule.StatusOK
	case out.Stopped:
		out.Status = schedule.StatusStopped
	default:
		out.Status = schedule.StatusFailed
	}
	return out
}

func intValue(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

func cloneEvent(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+4)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// scheduleStore is kept here so tests can point the manager at a temp dir
// without importing paths.
func scheduleStore() *schedule.Store { return schedule.NewStore(paths.Schedules()) }
