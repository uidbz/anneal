package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"

	"github.com/uidbz/anneal/core/improve"
	"github.com/uidbz/anneal/core/journey"
	"github.com/uidbz/anneal/core/paths"
)

// refreshActivity rebuilds the Activity view from the journey log and the
// improvement records on disk. It is deliberately a plain, refresh-on-demand
// view: no model calls and no background watchers.
func (s *shell) refreshActivity() {
	if s.activity == nil {
		return
	}
	s.activity.RemoveAll()
	s.activity.Add(newSectionLabel("JOURNEY"))
	s.activity.Add(newThemedText("Newest last · memory, skills, reviews, and improvements", colorMuted))

	entries := s.a.Journey.Tail(60)
	if len(entries) == 0 {
		s.activity.Add(newThemedText("Nothing yet — memory, skill, review, and improve events land here.", colorMuted))
	}
	for _, e := range entries {
		s.activity.Add(journeyRow(e))
	}

	s.activity.Add(vgap(spaceS))
	s.activity.Add(newSectionLabel("IMPROVEMENT RUNS"))
	runs := loadImprovementRuns(20)
	if len(runs) == 0 {
		s.activity.Add(newThemedText("No runs yet — improvements are recorded under "+paths.Improvements(), colorMuted))
	}
	for _, r := range runs {
		s.activity.Add(improvementCard(r))
	}
	s.activity.Refresh()
}

// journeyRow renders one timeline entry: time, kind badge, readable text.
func journeyRow(e journey.Entry) fyne.CanvasObject {
	timeLbl := newThemedText(journeyTime(e.TS), colorMuted)
	timeLbl.size = theme.SizeNameCaptionText
	kind := newThemedText(strings.ToUpper(e.Kind), colorMuted)
	kind.size = theme.SizeNameCaptionText
	kind.bold = true
	badge := newSurface(colorRaised,
		container.New(&padLayout{l: spaceS, r: spaceS, t: 0, b: 0}, kind)).border(colorBorder, 1)

	text := selectableLabel(journeyText(e), false)
	row := container.NewBorder(nil, nil,
		container.New(&rowLayout{gap: spaceS}, timeLbl, badge), nil,
		container.New(&padLayout{l: spaceS, r: 0, t: 2, b: 2}, text))
	return row
}

// improvementCard renders one improve.Result with its outcome and evidence.
func improvementCard(r improve.Result) fyne.CanvasObject {
	outcome := "Kept current harness"
	token := colorMuted
	if r.Successor != "" {
		outcome, token = "✔ Successor applied", colorOK
	}
	title := newThemedText(r.RunID, theme.ColorNameForeground)
	title.bold = true
	status := newThemedText(outcome, token)
	head := container.NewBorder(nil, nil, title, status)

	meta := fmt.Sprintf("k=%d · %s mode", r.K, r.Mode)
	if r.Mode == "" {
		meta = fmt.Sprintf("k=%d", r.K)
	}
	lines := []fyne.CanvasObject{head, newThemedText(meta, colorMuted)}
	if r.Task != "" {
		task := selectableLabel(r.Task, false)
		lines = append(lines, task)
	}
	if r.Note != "" {
		lines = append(lines, newThemedText("Note: "+r.Note, colorMuted))
	}
	if r.Successor != "" {
		lines = append(lines, newThemedText("Overlay: "+r.Successor, colorMuted))
	}
	if r.LibraryEntry != "" {
		lines = append(lines, newThemedText("Library: "+r.LibraryEntry, colorMuted))
	}
	for _, c := range r.Candidates {
		state := "probed"
		token := colorOK
		if c.Skipped != "" {
			state, token = "skipped: "+c.Skipped, colorWarn
		} else if c.Probe != nil {
			state = fmt.Sprintf("probed: %d steps", len(c.Probe.Steps))
		}
		lines = append(lines, container.New(&rowLayout{gap: spaceS},
			newThemedText(c.Kind, theme.ColorNameForeground),
			newThemedText(state, token)))
	}
	if r.Decisions != "" {
		lbl := selectableLabel(oneLine(r.Decisions), true)
		lines = append(lines, newSurface(colorCode,
			container.New(&padLayout{l: spaceS, r: spaceS, t: 2, b: 2}, lbl)).border(colorBorder, 1))
	}
	return newSurface(colorRaised, container.New(&padLayout{l: spaceM, r: spaceM, t: spaceS, b: spaceS},
		container.New(&columnLayout{gap: spaceXS}, lines...))).border(colorBorder, 1)
}

// journeyTime shortens an ISO timestamp to "YYYY-MM-DD HH:MM".
func journeyTime(ts string) string {
	if len(ts) >= 16 {
		return ts[:10] + " " + ts[11:16]
	}
	return ts
}

// journeyText is the human line for a journey entry (without the timestamp).
func journeyText(e journey.Entry) string {
	switch e.Kind {
	case "review":
		if v, ok := e.Attrs["lessons"]; ok {
			return fmt.Sprintf("Background review saved %v lessons", v)
		}
		return "Background review finished"
	case "improve":
		txt := "Improvement kept the current harness"
		if e.Attrs["successor"] == true {
			txt = "Improvement applied a successor harness"
		}
		if r, ok := e.Attrs["run"]; ok {
			txt += " (run " + fmt.Sprint(r) + ")"
		}
		return txt
	case "review-flag":
		return "Reviewer flagged a gap: " + shortText(oneLine(str(e.Attrs["gap"])), 160)
	case "harness-promote":
		return "Harness promoted from the library: " + str(e.Attrs["entry"])
	case "memory-add", "memory-patch", "memory-remove":
		verb := strings.TrimPrefix(e.Kind, "memory-")
		return "Memory " + verb + "ed for " + str(e.Attrs["target"])
	case "skill-create", "skill-update":
		verb := strings.TrimPrefix(e.Kind, "skill-")
		return "Skill " + verb + "d: " + str(e.Attrs["name"])
	default:
		var parts []string
		for k, v := range e.Attrs {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
		sort.Strings(parts)
		if len(parts) > 0 {
			return e.Kind + " · " + strings.Join(parts, " ")
		}
		return e.Kind
	}
}

// journeyLine formats one journey record for dialogs (timestamp + text).
func journeyLine(e journey.Entry) string {
	return journeyTime(e.TS) + "  " + journeyText(e)
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
