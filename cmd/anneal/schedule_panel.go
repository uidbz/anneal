package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/uidbz/anneal/core/schedule"
)

// schedulePanel is the Schedules view: a list of durable jobs with run/edit/
// pause/delete actions. Execution stays in anneald; the GUI talks to the
// local manager (management only) or the remote schedule.* protocol.
type schedulePanel struct {
	s      *shell
	object fyne.CanvasObject
	body   *fyne.Container
	status *themedText
	jobs   []schedule.Job
	busy   bool
}

func newSchedulePanel(s *shell) *schedulePanel {
	p := &schedulePanel{s: s, body: container.New(&columnLayout{gap: spaceS})}
	p.status = newThemedText("", colorMuted)

	content := container.New(&columnLayout{gap: spaceM},
		newSectionLabel("SCHEDULED TASKS"),
		p.status,
		p.body,
	)
	scroll := container.NewVScroll(container.New(&padLayout{l: spaceXL, r: spaceXL, t: spaceM, b: spaceM},
		container.New(&maxWidthLayout{max: 900, left: true}, content)))
	scroll.SetMinSize(fyne.NewSize(640, 360))
	p.object = scroll

	s.scheduleHeader.setActions(
		labeledButton("New task", theme.ContentAddIcon(), p.newTask),
		iconButton(theme.ViewRefreshIcon(), p.refresh),
	)
	s.scheduleHeader.setSubtitle("Jobs run in anneald; this view manages the durable store.")
	return p
}

func (p *schedulePanel) Object() fyne.CanvasObject { return p.object }

func (p *schedulePanel) refresh() {
	if p.s.view != viewSchedule {
		return
	}
	if p.busy {
		return
	}
	p.busy = true
	p.status.SetText("Loading scheduled jobs…")
	go func() {
		jobs, err := p.s.b.scheduleList()
		fyne.Do(func() {
			p.busy = false
			if err != nil {
				p.status.SetText("Schedules unavailable: " + err.Error())
				p.body.RemoveAll()
				p.body.Refresh()
				return
			}
			p.setJobs(jobs)
		})
	}()
}

func (p *schedulePanel) setJobs(jobs []schedule.Job) {
	schedule.SortJobs(jobs)
	p.jobs = jobs
	p.body.RemoveAll()
	if len(jobs) == 0 {
		p.status.SetText("No scheduled jobs yet. Create one here or ask the agent to schedule a task.")
		p.body.Add(newThemedText("Durable jobs are stored in ~/.anneal/schedules/jobs.json and run in isolated sessions when anneald is the scheduler owner.", colorMuted))
		p.body.Refresh()
		return
	}
	p.status.SetText(fmt.Sprintf("%d job(s) · click Run now to test one immediately", len(jobs)))
	if !p.s.b.remote() {
		p.status.SetText(p.status.Text() + " · local GUI is management-only; run anneald to execute")
	}
	for _, job := range jobs {
		p.body.Add(p.jobCard(job))
	}
	p.body.Refresh()
}

func (p *schedulePanel) jobCard(job schedule.Job) fyne.CanvasObject {
	title := job.Name
	if title == "" {
		title = job.ID
	}
	name := newThemedText(title, theme.ColorNameForeground)
	name.bold = true
	name.size = theme.SizeNameSubHeadingText
	id := newThemedText(job.ID, colorMuted)

	state := "enabled"
	token := colorOK
	if !job.IsEnabled() {
		state, token = "paused", colorMuted
	} else if job.LastStatus == schedule.StatusFailed {
		state, token = "last run failed", colorErr
	}
	badge := newBadge(state, colorRaised, token)
	head := container.NewBorder(nil, nil, nil,
		container.New(&rowLayout{gap: spaceS}, badge, id), name)

	next := "not scheduled"
	if job.NextRun != nil {
		next = job.NextRun.Local().Format("Mon 02 Jan 2006 15:04 MST")
	}
	meta := fmt.Sprintf("%s · next %s · %s", job.Schedule, next, firstNonEmptyText(job.Timezone, "system zone"))
	if job.LastStatus != "" {
		meta += fmt.Sprintf(" · last %s", job.LastStatus)
		if job.LastRunAt != nil {
			meta += " " + job.LastRunAt.Local().Format("02 Jan 15:04")
		}
	}
	if job.LastError != "" {
		meta += " · " + shortText(job.LastError, 100)
	}

	prompt := selectableLabel(shortText(strings.Join(strings.Fields(job.Prompt), " "), 260), false)
	prompt.Wrapping = fyne.TextWrapWord

	run := labeledButton("Run now", theme.MediaPlayIcon(), func() { p.run(job) })
	pause := labeledButton("Pause", theme.MediaPauseIcon(), func() { p.toggle(job) })
	if !job.IsEnabled() {
		pause = labeledButton("Resume", theme.MediaPlayIcon(), func() { p.toggle(job) })
	}
	edit := labeledButton("Edit", theme.DocumentCreateIcon(), func() { p.edit(job) })
	history := labeledButton("History", theme.HistoryIcon(), func() { p.history(job) })
	del := iconButton(theme.DeleteIcon(), func() { p.remove(job) })
	actions := container.New(&rowLayout{gap: spaceS}, run, pause, edit, history, del)

	content := container.New(&columnLayout{gap: spaceXS}, head, newThemedText(meta, colorMuted), prompt, actions)
	return newSurface(colorRaised,
		container.New(&padLayout{l: spaceM, r: spaceM, t: spaceM, b: spaceM}, content),
	).border(colorBorder, 1)
}

func (p *schedulePanel) run(job schedule.Job) {
	if p.busy {
		return
	}
	p.busy = true
	p.status.SetText("Queueing " + jobLabel(job) + "…")
	go func() {
		id, err := p.s.b.scheduleRun(job.ID)
		fyne.Do(func() {
			p.busy = false
			if err != nil {
				p.status.SetText("Run failed: " + err.Error())
				return
			}
			p.status.SetText("Queued manual run " + id + " for " + jobLabel(job))
			p.s.addNotice("Manual run queued: " + jobLabel(job))
		})
	}()
}

func (p *schedulePanel) toggle(job schedule.Job) {
	go func() {
		err := p.s.b.scheduleSetEnabled(job.ID, !job.IsEnabled())
		fyne.Do(func() {
			if err != nil {
				p.status.SetText("Update failed: " + err.Error())
				return
			}
			p.refresh()
		})
	}()
}

func (p *schedulePanel) remove(job schedule.Job) {
	d := dialog.NewConfirm("Delete scheduled task",
		"Delete "+jobLabel(job)+"? The run history stays in the ledger.",
		func(ok bool) {
			if !ok {
				return
			}
			go func() {
				err := p.s.b.scheduleRemove(job.ID)
				fyne.Do(func() {
					if err != nil {
						p.status.SetText("Delete failed: " + err.Error())
						return
					}
					p.s.addNotice("Deleted scheduled task " + jobLabel(job))
					p.refresh()
				})
			}()
		}, p.s.win)
	d.Show()
}

func (p *schedulePanel) history(job schedule.Job) {
	p.status.SetText("Loading run history…")
	go func() {
		runs, err := p.s.b.scheduleRuns(100)
		fyne.Do(func() {
			if err != nil {
				p.status.SetText("History unavailable: " + err.Error())
				return
			}
			var b strings.Builder
			n := 0
			for _, r := range runs {
				if r.JobID != job.ID {
					continue
				}
				n++
				fmt.Fprintf(&b, "%s  %-10s %-8s %4ds  %s\n",
					r.StartedAt.Local().Format("2006-01-02 15:04"), r.Trigger, r.Status, r.DurationS,
					firstNonEmptyText(r.SessionID, ""))
				if r.Error != "" {
					b.WriteString("  error: " + shortText(r.Error, 300) + "\n")
				}
				if r.Text != "" {
					b.WriteString("  " + shortText(strings.Join(strings.Fields(r.Text), " "), 500) + "\n")
				}
			}
			if n == 0 {
				b.WriteString("No runs recorded for " + jobLabel(job) + " yet.")
			}
			p.status.SetText(fmt.Sprintf("%d run(s) for %s", n, jobLabel(job)))
			p.s.showText("Runs — "+jobLabel(job), b.String())
		})
	}()
}

// edit opens the create/edit form. Empty job ID means create.
func (p *schedulePanel) newTask() { p.edit(schedule.Job{}) }

func (p *schedulePanel) edit(job schedule.Job) {
	create := job.ID == ""
	title := "Edit scheduled task"
	if create {
		title = "New scheduled task"
	}
	id := widget.NewEntry()
	id.SetText(job.ID)
	if !create {
		id.Disable()
	}
	name := widget.NewEntry()
	name.SetText(job.Name)
	expr := widget.NewEntry()
	expr.SetText(job.Schedule)
	expr.SetPlaceHolder("0 7 * * MON-FRI · every 2h · 2026-12-01T09:30:00")
	tz := widget.NewEntry()
	tz.SetText(job.Timezone)
	tz.SetPlaceHolder("system zone or Europe/Amsterdam")
	work := widget.NewEntry()
	work.SetText(job.Work)
	work.SetPlaceHolder("work-root-relative directory (optional)")

	session := widget.NewSelect([]string{schedule.SessionFresh, schedule.SessionSticky}, nil)
	session.SetSelected(firstNonEmptyText(job.Session, schedule.SessionFresh))
	mode := widget.NewSelect([]string{"dontAsk", "default", "plan", "acceptEdits", "bypassPermissions"}, nil)
	mode.SetSelected(firstNonEmptyText(job.PermissionMode, "dontAsk"))
	deliver := widget.NewEntry()
	deliver.SetText(strings.Join(job.Deliver, ","))
	deliver.SetPlaceHolder("all, ntfy, matrix-home")
	timeout := widget.NewEntry()
	if job.TimeoutS > 0 {
		timeout.SetText(strconv.Itoa(job.TimeoutS))
	}
	timeout.SetPlaceHolder("seconds (0 = config default)")
	enabled := widget.NewCheck("Enabled", nil)
	enabled.SetChecked(job.IsEnabled())
	prompt := widget.NewMultiLineEntry()
	prompt.SetText(job.Prompt)
	prompt.SetPlaceHolder("Self-contained task prompt. Fresh sessions have no chat history.")
	prompt.Wrapping = fyne.TextWrapWord
	prompt.SetMinRowsVisible(6)
	permission := widget.NewMultiLineEntry()
	if len(job.Permission) > 0 {
		b, _ := json.MarshalIndent(job.Permission, "", "  ")
		permission.SetText(string(b))
	} else {
		permission.SetText("{}")
	}
	permission.TextStyle = fyne.TextStyle{Monospace: true}
	permission.SetMinRowsVisible(4)

	form := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("ID", id),
			widget.NewFormItem("Name", name),
			widget.NewFormItem("Schedule", expr),
			widget.NewFormItem("Timezone", tz),
			widget.NewFormItem("Work dir", work),
			widget.NewFormItem("Session", session),
			widget.NewFormItem("Permission", mode),
			widget.NewFormItem("Deliver", deliver),
			widget.NewFormItem("Timeout", timeout),
			widget.NewFormItem("Enabled", enabled),
		),
		newSectionLabel("PROMPT"),
		prompt,
		newSectionLabel("PER-JOB PERMISSION RULES (JSON)"),
		newThemedText("Example: {\"gentoo\": \"allow\", \"bash\": {\"git status *\": \"allow\"}}", colorMuted),
		permission,
	)
	scroll := container.NewVScroll(form)
	scroll.SetMinSize(fyne.NewSize(720, 520))

	d := dialog.NewCustomConfirm(title, "Save", "Cancel", scroll, func(ok bool) {
		if !ok {
			return
		}
		out := job
		out.ID = strings.TrimSpace(id.Text)
		out.Name = strings.TrimSpace(name.Text)
		out.Schedule = strings.TrimSpace(expr.Text)
		out.Timezone = strings.TrimSpace(tz.Text)
		out.Work = strings.TrimSpace(work.Text)
		out.Session = session.Selected
		out.PermissionMode = mode.Selected
		out.Deliver = splitList(deliver.Text)
		out.TimeoutS = atoiDefault(timeout.Text, 0)
		out.Prompt = prompt.Text
		ev := enabled.Checked
		out.Enabled = &ev
		if strings.TrimSpace(permission.Text) != "" {
			if err := json.Unmarshal([]byte(permission.Text), &out.Permission); err != nil {
				p.s.addNotice("⚠ permission JSON: " + err.Error())
				return
			}
		}
		if out.Prompt == "" || out.Schedule == "" {
			p.s.addNotice("⚠ a scheduled task needs a prompt and a schedule")
			return
		}
		go func() {
			var (
				saved schedule.Job
				err   error
			)
			if create {
				saved, err = p.s.b.scheduleCreate(out)
			} else {
				saved, err = p.s.b.scheduleUpdate(out)
			}
			fyne.Do(func() {
				if err != nil {
					p.status.SetText("Save failed: " + err.Error())
					p.s.addNotice("⚠ schedule save: " + err.Error())
					return
				}
				p.s.addNotice("Saved scheduled task " + jobLabel(saved))
				p.refresh()
			})
		}()
	}, p.s.win)
	d.Resize(fyne.NewSize(800, 680))
	d.Show()
}

func jobLabel(job schedule.Job) string {
	if job.Name != "" {
		return job.Name + " (" + job.ID + ")"
	}
	if job.ID != "" {
		return job.ID
	}
	return "new task"
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func atoiDefault(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func firstNonEmptyText(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
