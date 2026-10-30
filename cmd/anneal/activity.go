package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
	"github.com/uidbz/anneal/core/improve"
	"github.com/uidbz/anneal/core/journey"
	"github.com/uidbz/anneal/core/paths"
)

// refreshActivity rebuilds the Activity tab from the journey log and the
// improvement records on disk. It is deliberately a plain, refresh-on-demand
// view: no model calls and no background watchers.
func (s *shell) refreshActivity() {
	if s.activity == nil {
		return
	}
	s.activity.RemoveAll()
	s.activity.Add(widget.NewLabelWithStyle("Journey (newest last)", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	journey := s.a.Journey.Tail(40)
	if len(journey) == 0 {
		empty := widget.NewLabel("empty — memory/skill/review/improve events land here")
		empty.Importance = widget.LowImportance
		s.activity.Add(empty)
	}
	for _, e := range journey {
		lbl := widget.NewLabel(journeyLine(e))
		lbl.Wrapping = fyne.TextWrapWord
		lbl.Selectable = true
		s.activity.Add(lbl)
	}
	s.activity.Add(widget.NewSeparator())
	s.activity.Add(widget.NewLabelWithStyle("Improvement runs", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	runs := loadImprovementRuns(20)
	if len(runs) == 0 {
		hint := widget.NewLabel("none yet — improve runs are recorded under " + paths.Improvements())
		hint.Importance = widget.LowImportance
		s.activity.Add(hint)
	}
	for _, r := range runs {
		title := fmt.Sprintf("%s — k=%d mode=%s", r.RunID, r.K, r.Mode)
		if title == "" {
			title = r.Dir
		}
		s.activity.Add(widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		body := r.Task
		if r.Note != "" {
			body += "\nnote: " + r.Note
		}
		if r.Successor != "" {
			body += "\noverlay: " + r.Successor
		}
		if r.LibraryEntry != "" {
			body += "\nlibrary: " + r.LibraryEntry
		}
		for _, c := range r.Candidates {
			state := "probed"
			if c.Skipped != "" {
				state = "skipped: " + c.Skipped
			} else if c.Probe != nil {
				state = fmt.Sprintf("probed: %d steps", len(c.Probe.Steps))
			}
			body += fmt.Sprintf("\n- %s: %s", c.Kind, state)
		}
		if r.Decisions != "" {
			body += "\ndecisions: " + shortText(oneLine(r.Decisions), 500)
		}
		lbl := widget.NewLabel(body)
		lbl.Wrapping = fyne.TextWrapWord
		lbl.Selectable = true
		s.activity.Add(lbl)
		s.activity.Add(widget.NewSeparator())
	}
	s.activity.Refresh()
}

// journeyLine formats one journey record as a readable line for the
// Activity tab (raw attr maps become kind-specific phrasing).
func journeyLine(e journey.Entry) string {
	ts := e.TS
	if len(ts) >= 16 {
		ts = ts[:10] + " " + ts[11:16]
	}
	txt := ts + "  " + e.Kind
	switch e.Kind {
	case "review":
		if v, ok := e.Attrs["lessons"]; ok {
			txt += fmt.Sprintf(" · %v lessons", v)
		}
	case "improve":
		if e.Attrs["successor"] == true {
			txt += " · successor applied"
		} else {
			txt += " · kept current harness"
		}
		if r, ok := e.Attrs["run"]; ok {
			txt += " (run " + fmt.Sprint(r) + ")"
		}
	case "review-flag":
		txt += " · " + shortText(oneLine(str(e.Attrs["gap"])), 120)
	default:
		var parts []string
		for k, v := range e.Attrs {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
		sort.Strings(parts)
		if len(parts) > 0 {
			txt += " · " + strings.Join(parts, " ")
		}
	}
	return txt
}

// loadImprovementRuns reads step.json records, newest first. Corrupt or
// partial records are skipped rather than failing the view.
func loadImprovementRuns(limit int) []improve.Result {
	entries, err := os.ReadDir(paths.Improvements())
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	out := make([]improve.Result, 0, len(names))
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(paths.Improvements(), name, "step.json"))
		if err != nil {
			continue
		}
		var r improve.Result
		if json.Unmarshal(b, &r) != nil || r.RunID == "" {
			continue
		}
		out = append(out, r)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
