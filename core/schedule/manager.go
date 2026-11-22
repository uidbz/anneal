package schedule

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/events"
)

// Executor runs one job. The app supplies it; tests use fakes.
type Executor interface {
	RunJob(ctx context.Context, job Job, rec RunRecord) (RunRecord, error)
}

// ErrLocked means another process already owns the scheduler.
var ErrLocked = fmt.Errorf("scheduler is already running in another process")

// Manager owns the job list, the single-owner scheduler loop, and the run
// ledger. Management methods are safe from any goroutine.
type Manager struct {
	mu    sync.Mutex
	cfg   config.Schedule
	store *Store
	bus   *events.Bus
	exec  Executor
	loc   *time.Location

	jobs    []Job
	lock    *Lock
	running bool
	runJob  string
	started bool
	cancel  context.CancelFunc

	wake     chan struct{}
	manualCh chan manualRequest

	now  func() time.Time
	tick time.Duration
}

type manualRequest struct {
	jobID string
	id    string
}

// New builds a manager. Start actually owns the scheduler; construction only
// prepares it for management APIs.
func New(cfg config.Schedule, store *Store, bus *events.Bus, exec Executor) (*Manager, error) {
	loc := time.Local
	if strings.TrimSpace(cfg.Timezone) != "" {
		l, err := time.LoadLocation(cfg.Timezone)
		if err != nil {
			return nil, fmt.Errorf("schedule timezone %q: %w", cfg.Timezone, err)
		}
		loc = l
	}
	if cfg.TickS <= 0 {
		cfg.TickS = 15
	}
	if cfg.DefaultTimeoutS <= 0 {
		cfg.DefaultTimeoutS = 1800
	}
	if cfg.MaxJobs <= 0 {
		cfg.MaxJobs = 50
	}
	if store == nil {
		return nil, fmt.Errorf("schedule: store is nil")
	}
	jobs, err := store.Load()
	if err != nil {
		return nil, err
	}
	return &Manager{
		cfg:      cfg,
		store:    store,
		bus:      bus,
		exec:     exec,
		loc:      loc,
		jobs:     jobs,
		wake:     make(chan struct{}, 1),
		manualCh: make(chan manualRequest, 32),
		now:      time.Now,
		tick:     time.Duration(cfg.TickS) * time.Second,
	}, nil
}

// SetExecutor attaches/replaces the runtime executor.
func (m *Manager) SetExecutor(exec Executor) {
	m.mu.Lock()
	m.exec = exec
	m.mu.Unlock()
}

// Enabled reports whether the schedule subsystem is configured on.
func (m *Manager) Enabled() bool { return m.cfg.Enabled }

// Started reports whether the due loop currently owns the scheduler lock.
func (m *Manager) Started() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started
}

// Start loads jobs, reconciles missed runs, takes the single-owner lock, and
// runs the due loop until ctx is done. It returns ErrLocked when another
// process already owns the scheduler.
func (m *Manager) Start(ctx context.Context) error {
	if !m.cfg.Enabled {
		return nil
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	lock, err := Acquire(m.store.LockPath)
	if err != nil {
		m.mu.Unlock()
		return ErrLocked
	}
	ctx, cancel := context.WithCancel(ctx)
	m.lock = lock
	m.cancel = cancel
	m.started = true
	m.mu.Unlock()

	jobs, err := m.store.Load()
	if err != nil {
		m.stop()
		return err
	}
	m.mu.Lock()
	m.jobs = jobs
	events := m.reconcileLocked()
	err = m.saveLocked()
	m.mu.Unlock()
	for _, ev := range events {
		m.emit(ev)
	}
	if err != nil {
		m.stop()
		return err
	}
	m.emit(m.StateEvent())

	go m.loop(ctx)
	return nil
}

// Stop releases the scheduler loop and lock.
func (m *Manager) Stop() { m.stop() }

func (m *Manager) stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.started = false
	lock := m.lock
	m.lock = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if lock != nil {
		lock.Close()
	}
}

func (m *Manager) loop(ctx context.Context) {
	tick := time.NewTicker(m.tick)
	defer tick.Stop()
	defer m.stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.runDue(ctx)
		case <-m.wake:
			m.runDue(ctx)
		case req := <-m.manualCh:
			m.runManual(ctx, req)
		}
	}
}

// planFor parses a job's schedule in its own timezone, falling back to the
// manager's default zone.
func (m *Manager) planFor(job Job) (*Plan, error) {
	loc, err := job.Location(m.loc)
	if err != nil {
		return nil, err
	}
	return Parse(job.Schedule, loc)
}

// reconcileLocked applies defaults and handles runs missed while the server
// was down. It must be called with m.mu held.
func (m *Manager) reconcileLocked() []any {
	now := m.now()
	var events []any
	for i := range m.jobs {
		j := &m.jobs[i]
		cfg := TimezoneConfig{Timezone: m.cfg.Timezone, GraceS: m.cfg.GraceS, DefaultTimeoutS: m.cfg.DefaultTimeoutS}
		j.Defaults(cfg)
		if !j.CreatedAt.IsZero() && j.UpdatedAt.IsZero() {
			j.UpdatedAt = j.CreatedAt
		}
		plan, err := m.planFor(*j)
		if err != nil {
			// A corrupt job must not stop the scheduler; surface it and
			// disable it until fixed.
			if j.IsEnabled() {
				j.SetEnabled(false)
				events = append(events, map[string]any{"type": "schedule.error", "job": j.Clone(),
					"error": "invalid schedule: " + err.Error()})
			}
			continue
		}
		if j.NextRun == nil {
			if plan.OneShot() {
				if plan.Once().After(now) {
					t := plan.Once()
					j.NextRun = &t
				} else {
					// A one-shot that elapsed while the server was down
					// should not stay enabled forever.
					j.SetEnabled(false)
				}
			} else {
				t := plan.Next(now)
				if !t.IsZero() {
					j.NextRun = &t
				}
			}
		}
		if j.NextRun != nil && !j.NextRun.After(now) {
			scheduled := *j.NextRun
			grace := time.Duration(j.GraceS) * time.Second
			if j.Missed != MissedRunOnce || now.Sub(scheduled) > grace {
				next := m.advance(plan, scheduled, now)
				events = append(events, map[string]any{"type": "schedule.missed", "job": j.Clone(),
					"scheduled_for": scheduled.UTC(), "next_run": next})
				j.NextRun = next
				if next == nil {
					j.SetEnabled(false)
				}
			}
		}
	}
	return events
}

// runDue picks at most one due job and runs it synchronously. The loop stays
// blocked during a run, which is intentional: one model/work slot per host.
func (m *Manager) runDue(ctx context.Context) {
	m.mu.Lock()
	if m.running || m.exec == nil {
		m.mu.Unlock()
		return
	}
	now := m.now()
	var (
		idx       = -1
		scheduled time.Time
	)
	for i := range m.jobs {
		j := &m.jobs[i]
		if !j.IsEnabled() || j.NextRun == nil || j.NextRun.After(now) {
			continue
		}
		if idx < 0 || j.NextRun.Before(scheduled) {
			idx = i
			scheduled = *j.NextRun
		}
	}
	if idx < 0 {
		m.mu.Unlock()
		return
	}
	job := m.jobs[idx].Clone()
	plan, err := m.planFor(job)
	if err != nil {
		m.mu.Unlock()
		return
	}
	next := m.advanceMode(plan, scheduled, now, job.Overlap)
	m.jobs[idx].NextRun = next
	m.jobs[idx].UpdatedAt = now
	if next == nil {
		m.jobs[idx].SetEnabled(false)
	}
	_ = m.saveLocked()
	m.mu.Unlock()

	m.execute(ctx, job, TriggerSchedule, &scheduled)
}

// runManual executes a job without touching its schedule (unless it was a
// one-shot that has now completed).
func (m *Manager) runManual(ctx context.Context, req manualRequest) {
	m.mu.Lock()
	if m.exec == nil {
		m.mu.Unlock()
		m.emit(map[string]any{"type": "schedule.error", "job_id": req.jobID,
			"error": "no executor configured"})
		return
	}
	if m.running {
		m.mu.Unlock()
		select {
		case m.manualCh <- req: // retry after the current run
		default:
			m.emit(map[string]any{"type": "schedule.error", "job_id": req.jobID,
				"error": "manual run queue is full"})
		}
		return
	}
	idx := m.indexLocked(req.jobID)
	if idx < 0 {
		m.mu.Unlock()
		m.emit(map[string]any{"type": "schedule.error", "job_id": req.jobID,
			"error": "job not found"})
		return
	}
	job := m.jobs[idx].Clone()
	m.mu.Unlock()
	m.execute(ctx, job, TriggerManual, nil)
}

func (m *Manager) execute(ctx context.Context, job Job, trigger string, scheduledFor *time.Time) {
	m.mu.Lock()
	exec := m.exec
	m.mu.Unlock()
	if exec == nil {
		m.emit(map[string]any{"type": "schedule.error", "job": job,
			"error": "no executor configured"})
		return
	}
	rec := RunRecord{
		ID:           NewID(job.ID),
		JobID:        job.ID,
		JobName:      job.Name,
		Trigger:      trigger,
		Status:       StatusRunning,
		ScheduledFor: scheduledFor,
		StartedAt:    m.now().UTC(),
	}
	timeout := time.Duration(job.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(m.cfg.DefaultTimeoutS) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	m.mu.Lock()
	m.running = true
	m.runJob = job.ID
	m.mu.Unlock()
	m.emit(map[string]any{"type": "schedule.started", "job": job, "record": rec})
	start := time.Now()

	result, err := exec.RunJob(runCtx, job, rec)
	finished := m.now().UTC()
	if result.ID == "" {
		result = rec
	}
	result.JobID, result.JobName, result.Trigger = job.ID, job.Name, trigger
	result.Deliver = append([]string(nil), job.Deliver...)
	if result.ScheduledFor == nil {
		result.ScheduledFor = scheduledFor
	}
	if result.StartedAt.IsZero() {
		result.StartedAt = rec.StartedAt
	}
	result.FinishedAt = &finished
	result.DurationS = int(time.Since(start).Seconds())
	result.Status = normalizeStatus(result, err, runCtx.Err())
	result.OK = result.Status == StatusOK
	if result.Error == "" && err != nil {
		result.Error = err.Error()
	}
	if result.Text != "" {
		result.Text = clipText(result.Text, 8000)
	}

	m.mu.Lock()
	if idx := m.indexLocked(job.ID); idx >= 0 {
		j := &m.jobs[idx]
		j.LastRunID = result.ID
		j.LastStatus = result.Status
		j.LastError = result.Error
		j.LastSessionID = result.SessionID
		j.LastRunAt = &finished
		if result.OK {
			j.LastOKAt = &finished
			j.RunCount++
		} else {
			j.FailCount++
		}
		if j.Session == SessionSticky && result.SessionID != "" {
			j.SessionID = result.SessionID
		}
		if j.NextRun == nil {
			j.SetEnabled(false)
		}
		j.UpdatedAt = finished
		result.NextRun = j.NextRun
	}
	m.running = false
	m.runJob = ""
	_ = m.saveLocked()
	m.mu.Unlock()

	_ = m.store.AppendRun(result)
	m.emit(map[string]any{"type": "schedule.finished", "job": job, "record": result})
}

func normalizeStatus(rec RunRecord, err error, ctxErr error) string {
	if rec.Status != "" && rec.Status != StatusRunning {
		return rec.Status
	}
	switch {
	case rec.Stopped:
		return StatusStopped
	case ctxErr == context.DeadlineExceeded:
		return StatusStopped
	case ctxErr == context.Canceled:
		return StatusInterrupted
	case err != nil:
		return StatusFailed
	case !rec.OK:
		return StatusFailed
	default:
		return StatusOK
	}
}

// List returns a copy of all jobs.
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]Job(nil), m.jobs...)
	return out
}

// Get returns one job.
func (m *Manager) Get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.indexLocked(id)
	if idx < 0 {
		return Job{}, false
	}
	return m.jobs[idx].Clone(), true
}

// Create adds a job. Omitted ids are derived from the name/prompt.
func (m *Manager) Create(job Job) (Job, error) {
	now := m.now().UTC()
	job.Defaults(TimezoneConfig{Timezone: m.cfg.Timezone, GraceS: m.cfg.GraceS, DefaultTimeoutS: m.cfg.DefaultTimeoutS})
	if strings.TrimSpace(job.ID) == "" {
		base := Slug(job.Name)
		if strings.TrimSpace(job.Name) == "" {
			base = Slug(job.Prompt)
		}
		job.ID = base
	}
	if err := job.Validate(TimezoneConfig{Timezone: m.cfg.Timezone, GraceS: m.cfg.GraceS, DefaultTimeoutS: m.cfg.DefaultTimeoutS}); err != nil {
		return Job{}, err
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	plan, err := m.planFor(job)
	if err != nil {
		return Job{}, err
	}
	if plan.OneShot() && !plan.Once().After(m.now()) {
		return Job{}, fmt.Errorf("one-shot schedule %q is in the past", job.Schedule)
	}
	next := plan.Next(m.now())
	if !next.IsZero() {
		t := next.UTC()
		job.NextRun = &t
	}

	m.mu.Lock()
	if len(m.jobs) >= m.cfg.MaxJobs {
		m.mu.Unlock()
		return Job{}, fmt.Errorf("schedule limit reached (%d jobs)", m.cfg.MaxJobs)
	}
	job.ID = m.uniqueIDLocked(job.ID)
	m.jobs = append(m.jobs, job)
	if err := m.saveLocked(); err != nil {
		m.jobs = m.jobs[:len(m.jobs)-1]
		m.mu.Unlock()
		return Job{}, err
	}
	m.signalLocked()
	out := job
	m.mu.Unlock()
	m.emit(map[string]any{"type": "schedule.created", "job": out})
	return out, nil
}

// Update replaces a job definition. Scheduling fields are recomputed when the
// expression or timezone changes.
func (m *Manager) Update(job Job) (Job, error) {
	job.Defaults(TimezoneConfig{Timezone: m.cfg.Timezone, GraceS: m.cfg.GraceS, DefaultTimeoutS: m.cfg.DefaultTimeoutS})
	if err := job.Validate(TimezoneConfig{Timezone: m.cfg.Timezone, GraceS: m.cfg.GraceS, DefaultTimeoutS: m.cfg.DefaultTimeoutS}); err != nil {
		return Job{}, err
	}
	m.mu.Lock()
	idx := m.indexLocked(job.ID)
	if idx < 0 {
		m.mu.Unlock()
		return Job{}, fmt.Errorf("job %q not found", job.ID)
	}
	old := m.jobs[idx]
	job.CreatedAt = old.CreatedAt
	job.UpdatedAt = m.now().UTC()
	job.RunCount, job.FailCount = old.RunCount, old.FailCount
	job.LastRunID, job.LastStatus, job.LastError = old.LastRunID, old.LastStatus, old.LastError
	job.LastSessionID, job.LastRunAt, job.LastOKAt = old.LastSessionID, old.LastRunAt, old.LastOKAt
	if job.Session == SessionSticky && job.SessionID == "" {
		job.SessionID = old.SessionID
	}
	if job.Session != SessionSticky {
		job.SessionID = ""
	}
	recompute := job.Schedule != old.Schedule || job.Timezone != old.Timezone || !job.IsEnabled()
	if !job.IsEnabled() {
		job.NextRun = nil
	}
	if job.IsEnabled() && (recompute || job.NextRun == nil) {
		plan, err := m.planFor(job)
		if err != nil {
			m.mu.Unlock()
			return Job{}, err
		}
		if plan.OneShot() && !plan.Once().After(m.now()) {
			m.mu.Unlock()
			return Job{}, fmt.Errorf("one-shot schedule %q is in the past", job.Schedule)
		}
		next := plan.Next(m.now())
		if !next.IsZero() {
			t := next.UTC()
			job.NextRun = &t
		} else {
			job.NextRun = nil
		}
	}
	m.jobs[idx] = job
	if err := m.saveLocked(); err != nil {
		m.jobs[idx] = old
		m.mu.Unlock()
		return Job{}, err
	}
	m.signalLocked()
	out := job
	m.mu.Unlock()
	m.emit(map[string]any{"type": "schedule.updated", "job": out})
	return out, nil
}

// Remove deletes a job. A running job cannot be removed.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	idx := m.indexLocked(id)
	if idx < 0 {
		m.mu.Unlock()
		return fmt.Errorf("job %q not found", id)
	}
	if m.running && m.runJob == id {
		m.mu.Unlock()
		return fmt.Errorf("job %q is running", id)
	}
	removed := m.jobs[idx]
	m.jobs = append(m.jobs[:idx], m.jobs[idx+1:]...)
	if err := m.saveLocked(); err != nil {
		m.jobs = append(m.jobs, removed)
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	m.emit(map[string]any{"type": "schedule.removed", "job": removed})
	return nil
}

// Pause disables a job without deleting it.
func (m *Manager) Pause(id string) (Job, error) {
	return m.setEnabled(id, false)
}

// Resume enables a job and schedules its next future occurrence.
func (m *Manager) Resume(id string) (Job, error) {
	return m.setEnabled(id, true)
}

func (m *Manager) setEnabled(id string, enabled bool) (Job, error) {
	m.mu.Lock()
	idx := m.indexLocked(id)
	if idx < 0 {
		m.mu.Unlock()
		return Job{}, fmt.Errorf("job %q not found", id)
	}
	job := m.jobs[idx].Clone()
	job.SetEnabled(enabled)
	job.UpdatedAt = m.now().UTC()
	if !enabled {
		job.NextRun = nil
	}
	if enabled {
		plan, err := m.planFor(job)
		if err != nil {
			m.mu.Unlock()
			return Job{}, err
		}
		if plan.OneShot() && !plan.Once().After(m.now()) {
			m.mu.Unlock()
			return Job{}, fmt.Errorf("one-shot schedule %q is in the past", job.Schedule)
		}
		next := plan.Next(m.now())
		if !next.IsZero() {
			t := next.UTC()
			job.NextRun = &t
		} else {
			job.NextRun = nil
		}
	}
	m.jobs[idx] = job
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return Job{}, err
	}
	m.signalLocked()
	m.mu.Unlock()
	kind := "schedule.paused"
	if enabled {
		kind = "schedule.resumed"
	}
	m.emit(map[string]any{"type": kind, "job": job})
	return job, nil
}

// RunNow queues a manual run; the result arrives as schedule.started /
// schedule.finished events. It returns the run record id.
func (m *Manager) RunNow(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.indexLocked(id) < 0 {
		return "", fmt.Errorf("job %q not found", id)
	}
	recID := NewID(id)
	select {
	case m.manualCh <- manualRequest{jobID: id, id: recID}:
		return recID, nil
	default:
		return "", fmt.Errorf("manual run queue is full")
	}
}

// Runs returns the run ledger, newest first.
func (m *Manager) Runs(limit int) ([]RunRecord, error) {
	return m.store.Runs(limit)
}

// RunsSince returns records started at or after unix seconds.
func (m *Manager) RunsSince(unix int64, limit int) ([]RunRecord, error) {
	return m.store.FilterRunsSince(unix, limit)
}

// StateEvent is the replay snapshot for protocol clients.
func (m *Manager) StateEvent() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return map[string]any{
		"type":    "schedule.state",
		"jobs":    append([]Job(nil), m.jobs...),
		"running": m.running,
		"job":     m.runJob,
		"now":     m.now().UTC(),
	}
}

// Busy reports whether a job is executing now.
func (m *Manager) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// NextRun reports the earliest enabled next run.
func (m *Manager) NextRun() (time.Time, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var (
		best time.Time
		id   string
		ok   bool
	)
	for _, j := range m.jobs {
		if !j.IsEnabled() || j.NextRun == nil {
			continue
		}
		if !ok || j.NextRun.Before(best) {
			best = *j.NextRun
			id = j.ID
			ok = true
		}
	}
	return best, id, ok
}

func (m *Manager) indexLocked(id string) int {
	for i := range m.jobs {
		if m.jobs[i].ID == id {
			return i
		}
	}
	return -1
}

func (m *Manager) uniqueIDLocked(base string) string {
	if m.indexLocked(base) < 0 {
		return base
	}
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if m.indexLocked(cand) < 0 {
			return cand
		}
	}
	return base + "-" + time.Now().UTC().Format("150405")
}

// advance computes the next firing after the scheduled instant. Missed
// recurring slots are skipped (the safe default) so a long outage does not
// produce a burst of runs. One-shots return nil.
func (m *Manager) advance(plan *Plan, scheduled, now time.Time) *time.Time {
	next := m.nextAfter(plan, scheduled, now)
	if next.IsZero() {
		return nil
	}
	t := next.UTC()
	return &t
}

// advanceMode is advance with the job's overlap policy. "queue" keeps one
// missed slot pending (it runs again after the current run finishes);
// "skip" jumps to the next future slot so a long outage never bursts.
func (m *Manager) advanceMode(plan *Plan, scheduled, now time.Time, overlap string) *time.Time {
	if overlap != OverlapQueue || plan == nil {
		return m.advance(plan, scheduled, now)
	}
	if plan.OneShot() {
		return nil
	}
	next := plan.Next(scheduled)
	if next.IsZero() {
		return nil
	}
	t := next.UTC()
	return &t
}

func (m *Manager) nextAfter(plan *Plan, from, now time.Time) time.Time {
	if plan == nil {
		return time.Time{}
	}
	if plan.OneShot() {
		if plan.Once().After(now) {
			return plan.Once()
		}
		return time.Time{}
	}
	next := plan.Next(from)
	for i := 0; i < 10000 && !next.IsZero() && !next.After(now); i++ {
		next = plan.Next(next)
	}
	return next
}

func (m *Manager) saveLocked() error {
	return m.store.Save(append([]Job(nil), m.jobs...))
}

func (m *Manager) signalLocked() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) emit(v any) {
	if m.bus != nil {
		m.bus.Emit(v)
	}
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… (truncated)"
}

// SortJobs sorts by next run (nil last), then id.
func SortJobs(jobs []Job) {
	sort.SliceStable(jobs, func(i, j int) bool {
		a, b := jobs[i], jobs[j]
		if a.NextRun == nil && b.NextRun == nil {
			return a.ID < b.ID
		}
		if a.NextRun == nil {
			return false
		}
		if b.NextRun == nil {
			return true
		}
		if !a.NextRun.Equal(*b.NextRun) {
			return a.NextRun.Before(*b.NextRun)
		}
		return a.ID < b.ID
	})
}
