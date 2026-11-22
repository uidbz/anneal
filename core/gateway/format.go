package gateway

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/uidbz/anneal/core/client"
)

func formatRun(server string, ev client.Event) Message {
	ok, _ := ev["ok"].(bool)
	stopped, _ := ev["stopped"].(bool)
	status := "done"
	switch {
	case stopped:
		status = "stopped"
	case !ok:
		status = "failed"
	}
	text := str(ev["text"])
	if text == "" {
		text = str(ev["error"])
	}
	if text == "" {
		text = "(no output)"
	}
	return Message{
		Title:    fmt.Sprintf("anneal (%s) - %s", server, status),
		Text:     trimText(text, 3500),
		Priority: priorityFor(status),
		Tags:     tagsFor(status),
		Markdown: true,
	}
}

// statusWord is header-safe (HTTP titles are not a reliable emoji channel;
// ntfy renders Tags instead).
func statusWord(status string) string {
	switch status {
	case "ok", "done":
		return "done"
	case "stopped":
		return "stopped"
	case "missed":
		return "missed"
	case "skipped":
		return "skipped"
	case "interrupted":
		return "interrupted"
	default:
		return "failed"
	}
}

func priorityFor(status string) int {
	if status == "ok" || status == "done" {
		return 3
	}
	return 4
}

func tagsFor(status string) []string {
	switch status {
	case "ok", "done":
		return []string{"white_check_mark"}
	case "stopped", "interrupted":
		return []string{"stop_sign"}
	default:
		return []string{"x"}
	}
}

// silent reports the Hermes-style delivery suppression tokens.
func silent(text string) bool {
	s := strings.ToUpper(strings.TrimSpace(text))
	switch s {
	case "[SILENT]", "SILENT", "NO_REPLY", "NO REPLY":
		return true
	}
	return false
}

func trimText(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "\n… (truncated)"
}

func summarizeJob(j map[string]any) string {
	id := str(j["id"])
	name := firstString(j["name"], id)
	enabled := true
	if v, ok := j["enabled"].(bool); ok {
		enabled = v
	}
	status := "enabled"
	if !enabled {
		status = "paused"
	}
	next := str(j["next_run"])
	if next == "" {
		next = "—"
	}
	last := str(j["last_status"])
	if last == "" {
		last = "never"
	}
	return fmt.Sprintf("%s (%s) [%s] next=%s last=%s", name, id, status, next, last)
}
