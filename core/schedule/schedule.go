// Package schedule owns durable scheduled tasks: job definitions, schedule
// parsing, next-run calculation, and the job/run store. The runtime executor
// lives in core/app so this package stays free of the agent/Fyne dependencies.
package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/uidbz/anneal/core/permission"
)

// Session modes.
const (
	SessionFresh  = "fresh"  // a new session per run (default)
	SessionSticky = "sticky" // reuse the job's session_id between runs
)

// Overlap policies when the previous occurrence is still running.
const (
	OverlapSkip  = "skip"  // drop this occurrence (default)
	OverlapQueue = "queue" // run it after the active occurrence
)

// Missed policies on server restart.
const (
	MissedSkip    = "skip"     // move to the next future occurrence
	MissedRunOnce = "run_once" // run one catch-up inside the grace window
)

// Run statuses.
const (
	StatusRunning     = "running"
	StatusOK          = "ok"
	StatusFailed      = "failed"
	StatusStopped     = "stopped"
	StatusSkipped     = "skipped"
	StatusMissed      = "missed"
	StatusInterrupted = "interrupted"
)

// Trigger values.
const (
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
	TriggerCatchUp  = "catchup"
)

var (
	idRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	atomRe = regexp.MustCompile(`^(?:[<>=~!]+)?[A-Za-z0-9][A-Za-z0-9+_.:/-]*$`)
)

// Job is one durable scheduled task. The prompt must be self-contained: each
// fresh run starts with no chat history.
type Job struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Prompt   string `json:"prompt"`
	Schedule string `json:"schedule"` // cron, "every 2h", "30m", or ISO timestamp
	Timezone string `json:"timezone,omitempty"`
	Work     string `json:"work,omitempty"` // work-root-relative directory

	// Session is "fresh" (default) or "sticky". Sticky jobs persist the
	// session id returned after the first run.
	Session   string `json:"session,omitempty"`
	SessionID string `json:"session_id,omitempty"`

	// PermissionMode applies to this job only; the safe default is dontAsk
	// (reads work, writes/bash need explicit allow rules).
	PermissionMode string            `json:"permission_mode,omitempty"`
	Permission     permission.Config `json:"permission,omitempty"`

	Deliver []string `json:"deliver,omitempty"` // transport names: ntfy, matrix, ...
	Overlap string   `json:"overlap,omitempty"` // skip (default) | queue
	Missed  string   `json:"missed,omitempty"`  // skip (default) | run_once

	GraceS           int `json:"grace_s,omitempty"`            // catch-up window
	TimeoutS         int `json:"timeout_s,omitempty"`          // hard run limit
	ApprovalTimeoutS int `json:"approval_timeout_s,omitempty"` // ask-mode cards

	Enabled *bool      `json:"enabled,omitempty"` // nil = true
	NextRun *time.Time `json:"next_run,omitempty"`

	LastRunID     string     `json:"last_run_id,omitempty"`
	LastStatus    string     `json:"last_status,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	LastSessionID string     `json:"last_session_id,omitempty"`
	LastRunAt     *time.Time `json:"last_run_at,omitempty"`
	LastOKAt      *time.Time `json:"last_ok_at,omitempty"`

	Origin    string    `json:"origin,omitempty"` // chat | cli | api
	RunCount  int       `json:"run_count,omitempty"`
	FailCount int       `json:"fail_count,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// IsEnabled reports the effective enabled state (omitted means true).
func (j Job) IsEnabled() bool { return j.Enabled == nil || *j.Enabled }

// SetEnabled writes the pointer form used by JSON.
func (j *Job) SetEnabled(v bool) { j.Enabled = &v }

// RunRecord is one execution attempt. Records are appended to runs.jsonl and
// also returned by the API so notification gateways can catch up after an
// outage.
type RunRecord struct {
	ID           string         `json:"id"`
	JobID        string         `json:"job_id"`
	JobName      string         `json:"job_name,omitempty"`
	Trigger      string         `json:"trigger"`
	Status       string         `json:"status"`
	ScheduledFor *time.Time     `json:"scheduled_for,omitempty"`
	StartedAt    time.Time      `json:"started_at"`
	FinishedAt   *time.Time     `json:"finished_at,omitempty"`
	SessionID    string         `json:"session_id,omitempty"`
	Run          int            `json:"run,omitempty"`
	OK           bool           `json:"ok"`
	Stopped      bool           `json:"stopped,omitempty"`
	Error        string         `json:"error,omitempty"`
	Text         string         `json:"text,omitempty"`
	Usage        map[string]any `json:"usage,omitempty"`
	DurationS    int            `json:"duration_s,omitempty"`
	NextRun      *time.Time     `json:"next_run,omitempty"`
	Deliver      []string       `json:"deliver,omitempty"`
	Delivered    bool           `json:"delivered,omitempty"`
}

// Defaults fills omitted policy fields. It does not assign IDs or next runs.
func (j *Job) Defaults(cfg TimezoneConfig) {
	if j.Session == "" {
		j.Session = SessionFresh
	}
	if j.Overlap == "" {
		j.Overlap = OverlapSkip
	}
	if j.Missed == "" {
		j.Missed = MissedSkip
	}
	if j.PermissionMode == "" {
		j.PermissionMode = string(permission.ModeDontAsk)
	}
	if j.Timezone == "" {
		j.Timezone = cfg.Timezone
	}
	if j.GraceS <= 0 {
		j.GraceS = cfg.GraceS
	}
	if j.TimeoutS <= 0 {
		j.TimeoutS = cfg.DefaultTimeoutS
	}
	if len(j.Deliver) == 0 {
		j.Deliver = []string{"all"}
	}
	if j.Enabled == nil {
		j.SetEnabled(true)
	}
}

// TimezoneConfig is the subset of config needed for defaults.
type TimezoneConfig struct {
	Timezone        string
	GraceS          int
	DefaultTimeoutS int
}

// Location resolves the job timezone, falling back to the supplied default.
func (j Job) Location(fallback *time.Location) (*time.Location, error) {
	tz := strings.TrimSpace(j.Timezone)
	if tz == "" {
		if fallback != nil {
			return fallback, nil
		}
		return time.Local, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("timezone %q: %w", tz, err)
	}
	return loc, nil
}

// Validate checks everything needed to safely persist/run a job.
func (j *Job) Validate(cfg TimezoneConfig) error {
	if j.ID != "" && !idRe.MatchString(j.ID) {
		return fmt.Errorf("id %q must match %s", j.ID, idRe)
	}
	if strings.TrimSpace(j.Prompt) == "" {
		return fmt.Errorf("prompt is required")
	}
	if strings.TrimSpace(j.Schedule) == "" {
		return fmt.Errorf("schedule is required")
	}
	loc, err := j.Location(nil)
	if err != nil {
		return err
	}
	if _, err := Parse(j.Schedule, loc); err != nil {
		return err
	}
	switch j.Session {
	case "", SessionFresh, SessionSticky:
	default:
		return fmt.Errorf("session must be %q or %q", SessionFresh, SessionSticky)
	}
	switch j.Overlap {
	case "", OverlapSkip, OverlapQueue:
	default:
		return fmt.Errorf("overlap must be %q or %q", OverlapSkip, OverlapQueue)
	}
	switch j.Missed {
	case "", MissedSkip, MissedRunOnce:
	default:
		return fmt.Errorf("missed must be %q or %q", MissedSkip, MissedRunOnce)
	}
	if j.PermissionMode != "" && !permission.ValidMode(j.PermissionMode) {
		return fmt.Errorf("permission_mode %q is invalid", j.PermissionMode)
	}
	if err := ValidateWork(j.Work); err != nil {
		return err
	}
	if j.TimeoutS < 0 || j.GraceS < 0 || j.ApprovalTimeoutS < 0 {
		return fmt.Errorf("timeouts must not be negative")
	}
	for _, d := range j.Deliver {
		if strings.TrimSpace(d) == "" {
			return fmt.Errorf("deliver entries must not be empty")
		}
	}
	return nil
}

// ValidateWork accepts a work-root-relative path: no absolute paths, drive
// forms, NULs, or ".." segments. "" is the work root.
func ValidateWork(work string) error {
	if work == "" {
		return nil
	}
	if strings.ContainsRune(work, 0) || strings.Contains(work, `\`) {
		return fmt.Errorf("work %q contains an invalid character", work)
	}
	if strings.HasPrefix(work, "/") || filepath.IsAbs(work) {
		return fmt.Errorf("work %q must be relative to the work root", work)
	}
	for _, seg := range strings.Split(work, "/") {
		if seg == ".." {
			return fmt.Errorf("work %q escapes the work root", work)
		}
	}
	return nil
}

// NormalizeWork returns the slash-cleaned work path ("" stays the root).
func NormalizeWork(work string) string {
	work = strings.TrimSpace(work)
	if work == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(work))
}

// Slug makes a stable, human-readable id from a name or prompt.
func Slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == ' ' || r == '-' || r == '_' || r == '.' || r == '/':
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
		if b.Len() >= 48 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "job"
	}
	if !idRe.MatchString(out) {
		out = "job"
	}
	return out
}

// NewID returns a unique run record id.
func NewID(jobID string) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s-%s", jobID, time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(b))
}

// Clone deep-copies via JSON (small documents, simple correctness).
func (j Job) Clone() Job {
	data, err := json.Marshal(j)
	if err != nil {
		return j
	}
	var out Job
	if json.Unmarshal(data, &out) != nil {
		return j
	}
	return out
}

// ValidAtom reports whether a package atom is safe to pass as one argv
// element to emerge (no shell metacharacters, no option injection).
func ValidAtom(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	return atomRe.MatchString(s)
}
