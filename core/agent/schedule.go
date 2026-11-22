package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/uidbz/anneal/core/schedule"

	"github.com/deepnoodle-ai/dive"
)

// ScheduleAPI is the scheduled-task surface exposed to the agent. It is
// implemented by core/schedule.Manager and attached by core/app.
type ScheduleAPI interface {
	List() []schedule.Job
	Get(id string) (schedule.Job, bool)
	Create(job schedule.Job) (schedule.Job, error)
	Update(job schedule.Job) (schedule.Job, error)
	Remove(id string) error
	Pause(id string) (schedule.Job, error)
	Resume(id string) (schedule.Job, error)
	RunNow(id string) (string, error)
	Runs(limit int) ([]schedule.RunRecord, error)
	Started() bool
}

// SetScheduler attaches the scheduled-task manager (nil = no schedule tool).
func (r *Runner) SetScheduler(s ScheduleAPI) { r.sched = s }

type scheduleInput struct {
	Action string        `json:"action" description:"list, get, create, update, pause, resume, run, remove, runs" enum:"list,get,create,update,pause,resume,run,remove,runs"`
	ID     string        `json:"id,omitempty" description:"job id for get/update/pause/resume/run/remove"`
	Job    *schedule.Job `json:"job,omitempty" description:"full job document for create/update"`
	Limit  int           `json:"limit,omitempty" description:"max run records for runs (default 20)"`
}

func (r *Runner) scheduleTool() dive.Tool {
	return dive.FuncTool("schedule_manage",
		"Create and manage durable scheduled tasks. Jobs run in fresh sessions by default, so the prompt must be completely self-contained (no chat history). Schedule formats: five-field cron (0 7 * * MON-FRI), \"every 2h\", and one-shot \"30m\" or an ISO timestamp. Set timezone when the server default is not right. The default permission_mode is dontAsk: reads work, but writes/bash need explicit allow rules. Optional deliver names notification transports (ntfy, matrix, all). Current local time is "+
			time.Now().Format("2006-01-02 15:04 MST")+"; always return and confirm next_run after creating a job.",
		func(ctx context.Context, in *scheduleInput) (*dive.ToolResult, error) {
			if r.sched == nil {
				return dive.NewToolResultError("schedule_manage: scheduler is not available in this process"), nil
			}
			action := strings.ToLower(strings.TrimSpace(in.Action))
			switch action {
			case "":
				action = "list"
			case "list", "get", "create", "update", "pause", "resume", "run", "remove", "runs":
			default:
				return dive.NewToolResultError("schedule_manage: action must be list|get|create|update|pause|resume|run|remove|runs"), nil
			}
			if action != "list" && action != "create" && action != "runs" && strings.TrimSpace(in.ID) == "" {
				return dive.NewToolResultError("schedule_manage: id is required for " + action), nil
			}

			switch action {
			case "list":
				jobs := r.sched.List()
				schedule.SortJobs(jobs)
				return scheduleText(jobs), nil
			case "get":
				job, ok := r.sched.Get(in.ID)
				if !ok {
					return dive.NewToolResultError("schedule_manage: job " + in.ID + " not found"), nil
				}
				b, _ := json.MarshalIndent(job, "", "  ")
				runs, _ := r.sched.Runs(10)
				var filtered []schedule.RunRecord
				for _, rec := range runs {
					if rec.JobID == job.ID {
						filtered = append(filtered, rec)
					}
				}
				rb, _ := json.MarshalIndent(filtered, "", "  ")
				return dive.NewToolResultText(fmt.Sprintf("job:\n%s\nrecent runs:\n%s", b, rb)), nil
			case "create":
				if in.Job == nil {
					return dive.NewToolResultError("schedule_manage: create needs a job object"), nil
				}
				job, err := r.sched.Create(*in.Job)
				if err != nil {
					return dive.NewToolResultError("schedule_manage: " + err.Error()), nil
				}
				return scheduleCreatedText(job), nil
			case "update":
				if in.Job == nil {
					return dive.NewToolResultError("schedule_manage: update needs a job object"), nil
				}
				in.Job.ID = in.ID
				job, err := r.sched.Update(*in.Job)
				if err != nil {
					return dive.NewToolResultError("schedule_manage: " + err.Error()), nil
				}
				return scheduleCreatedText(job), nil
			case "pause":
				job, err := r.sched.Pause(in.ID)
				if err != nil {
					return dive.NewToolResultError("schedule_manage: " + err.Error()), nil
				}
				return dive.NewToolResultText("paused: " + formatJob(job)), nil
			case "resume":
				job, err := r.sched.Resume(in.ID)
				if err != nil {
					return dive.NewToolResultError("schedule_manage: " + err.Error()), nil
				}
				return dive.NewToolResultText("resumed: " + formatJob(job)), nil
			case "remove":
				if err := r.sched.Remove(in.ID); err != nil {
					return dive.NewToolResultError("schedule_manage: " + err.Error()), nil
				}
				return dive.NewToolResultText("removed: " + in.ID), nil
			case "run":
				if !r.sched.Started() {
					return dive.NewToolResultError("schedule_manage: scheduler is not running (enable schedule.enabled and start anneald)"), nil
				}
				id, err := r.sched.RunNow(in.ID)
				if err != nil {
					return dive.NewToolResultError("schedule_manage: " + err.Error()), nil
				}
				return dive.NewToolResultText("queued manual run " + id + " for " + in.ID + "; results arrive as schedule.started/schedule.finished events"), nil
			case "runs":
				limit := in.Limit
				if limit <= 0 {
					limit = 20
				}
				runs, err := r.sched.Runs(limit)
				if err != nil {
					return dive.NewToolResultError("schedule_manage: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(runs, "", "  ")
				return dive.NewToolResultText(string(b)), nil
			}
			return dive.NewToolResultError("schedule_manage: unreachable"), nil
		},
		dive.WithFuncToolAnnotations(&dive.ToolAnnotations{Title: "Scheduled tasks"}))
}

func scheduleText(jobs []schedule.Job) *dive.ToolResult {
	if len(jobs) == 0 {
		return dive.NewToolResultText("no scheduled jobs")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d scheduled jobs (now %s):\n", len(jobs), time.Now().Format(time.RFC3339))
	for _, j := range jobs {
		sb.WriteString("- ")
		sb.WriteString(formatJob(j))
		sb.WriteByte('\n')
	}
	return dive.NewToolResultText(sb.String())
}

func scheduleCreatedText(job schedule.Job) *dive.ToolResult {
	b, _ := json.MarshalIndent(job, "", "  ")
	return dive.NewToolResultText("saved: " + formatJob(job) + "\n" + string(b))
}

func formatJob(j schedule.Job) string {
	status := "enabled"
	if !j.IsEnabled() {
		status = "paused"
	}
	next := "—"
	if j.NextRun != nil {
		next = j.NextRun.Local().Format(time.RFC3339)
	}
	name := j.Name
	if name == "" {
		name = j.ID
	}
	last := j.LastStatus
	if last == "" {
		last = "never"
	}
	return fmt.Sprintf("%s (%s) [%s] schedule=%q next=%s last=%s runs=%d fails=%d",
		name, j.ID, status, j.Schedule, next, last, j.RunCount, j.FailCount)
}
