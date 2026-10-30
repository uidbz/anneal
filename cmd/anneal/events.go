package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

func (s *shell) handleEvent(e map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "anneal: ui event panic: %v\n", r)
		}
	}()
	switch str(e["type"]) {
	case "provider.state":
		h := obj(e["health"])
		state := str(h["state"])
		switch state {
		case "online":
			s.modelLbl.SetText(fmt.Sprintf("%s · %s ctx",
				trunc(str(h["model"]), 22), humanK(int(num(h["ctx_window"])))))
			s.modelDot.setToken(colorOK)
			s.cmp.setModel(str(h["model"]), int(num(h["ctx_window"])))
		case "connecting", "probing":
			s.modelLbl.SetText("model: " + state + "…")
			s.modelDot.setToken(colorWarn)
		default:
			msg := str(h["error"])
			if msg != "" {
				s.modelLbl.SetText("model: " + state + " — " + msg)
			} else {
				s.modelLbl.SetText("model: " + state)
			}
			s.modelDot.setToken(colorErr)
		}
		s.chipModel.Show()
	case "tie.state":
		st := obj(e["state"])
		switch {
		case !boolean(st["enabled"]):
			s.chipTie.Hide()
		case boolean(st["online"]):
			s.tie.SetText("index: " + str(st["collection"]))
			s.tieDot.setToken(colorOK)
			s.chipTie.Show()
		default:
			s.tie.SetText("index: offline")
			s.tieDot.setToken(colorErr)
			s.chipTie.Show()
		}
	case "mcp.state":
		servers := list(e["servers"])
		up, tools := 0, 0
		for _, raw := range servers {
			srv := obj(raw)
			if boolean(srv["connected"]) {
				up++
			}
			tools += int(num(srv["tools"]))
		}
		if len(servers) > 0 {
			s.mcp.SetText(fmt.Sprintf("mcp: %d/%d · %d tools", up, len(servers), tools))
			switch {
			case up == len(servers):
				s.mcpDot.setToken(colorOK)
			case up > 0:
				s.mcpDot.setToken(colorWarn)
			default:
				s.mcpDot.setToken(colorErr)
			}
			s.chipMCP.Show()
		} else {
			s.chipMCP.Hide()
		}
	case "run.started":
		s.setRunState("Running…", colorWarn, false)
		s.usage.SetText("")
		s.cmp.setRunning(true)
	case "run.busy":
		s.status.SetText("A run is already active")
	case "text.delta":
		s.ensureTail()
		s.tailBuf += str(e["text"])
		s.flushTail()
	case "thinking.delta":
		s.status.SetText("Thinking…")
		s.runDot.setToken(colorWarn)
	case "message.final":
		s.closeTail(str(e["text"]))
	case "tool.start":
		s.closeTail("")
		s.addTool(str(e["id"]), str(e["name"]), str(e["input"]))
	case "tool.result":
		s.finishTool(str(e["id"]), str(e["name"]), str(e["preview"]), boolean(e["error"]))
	case "run.finished":
		s.closeTail("")
		u := obj(e["usage"])
		s.usage.SetText(fmt.Sprintf("in %s · out %s · ctx %d%%",
			humanK(int(num(u["tin"]))), humanK(int(num(u["tout"]))), int(num(e["ctx_pct"]))))
		s.inspector.tin = int(num(u["tin"]))
		s.inspector.tout = int(num(u["tout"]))
		s.inspector.ctxPct = int(num(e["ctx_pct"]))
		s.inspector.refresh()
		s.refreshSessionMeta()
		switch {
		case boolean(e["stopped"]):
			s.setRunState("Stopped", colorWarn, false)
		case boolean(e["ok"]):
			s.setRunState("Ready", colorOK, false)
		default:
			s.setRunState("Error", colorErr, true)
			s.usage.SetText("")
			if msg := str(e["error"]); msg != "" {
				s.addNotice("⚠ " + msg)
			}
		}
		s.cmp.setRunning(false)
		s.scroll.ScrollToBottom()
		if s.autoQuit {
			time.AfterFunc(1500*time.Millisecond, func() { fyne.Do(func() { s.app.Quit() }) })
		}
	case "session.started":
		s.sessionID = str(e["id"])
		s.title = ""
		s.resetChat()
		s.header.setTitle("New session")
		s.win.SetTitle("Anneal")
		s.entry.SetText("")
		s.win.Canvas().Focus(s.entry)
		s.side.refresh()
		s.inspector.refresh()
	case "session.opened":
		s.resetChat()
		for _, raw := range list(e["blocks"]) {
			s.renderBlock(obj(raw))
		}
		if len(list(e["blocks"])) == 0 {
			s.addNotice("welcome — describe a task below, or /help for commands")
		}
		s.sessionID = str(e["id"])
		s.title = str(e["title"])
		s.setWindowTitle()
		s.side.refresh()
		s.inspector.refresh()
		s.scroll.ScrollToBottom()
	case "session.error":
		s.notice(noticeError, fmt.Sprintf("Session %s: %s", str(e["id"]), str(e["error"])))
	case "mode.changed":
		s.mode = str(e["mode"])
		s.cmp.setMode(s.mode)
		text, fill, fg := modeBadge(s.mode)
		s.header.setBadge(text, fill, fg)
		s.inspector.refresh()
	case "todo.updated":
		s.renderTodos(list(e["items"]))
	case "perm.request":
		s.addPermEvent(e)
	case "perm.saved":
		s.notice(noticeSuccess, fmt.Sprintf("Permission saved: %s %s", str(e["action"]),
			strings.Join(stringList(e["resources"]), ", ")))
	case "perm.warning":
		s.notice(noticeWarn, "Permission save failed: "+str(e["error"]))
	case "perm.resolved":
		s.removeCard(str(e["id"]))
	case "memory.stage":
		s.addMemoryCard(e)
	case "skill.stage":
		s.addSkillCard(e)
	case "harness.overlay":
		s.inspector.refresh()
	case "compaction.done":
		s.inspector.compacted++
		s.notice(noticeInfo, fmt.Sprintf("Context compacted: %d → %d tokens (%d messages)",
			int(num(e["before"])), int(num(e["after"])), int(num(e["msgs"]))))
	case "review.start":
		s.setRunState("Reviewing…", colorWarn, false)
		s.progress("review", "Reviewing the session in the background…")
	case "review.skipped":
		s.setRunState("Ready", colorOK, false)
		if str(e["reason"]) == "preempted by user" {
			s.finishProgress("review", noticeInfo, "")
		} else {
			s.finishProgress("review", noticeInfo, "Background review skipped: "+str(e["reason"]))
		}
	case "review.flag":
		s.notice(noticeWarn, "Reviewer flagged a harness gap: "+str(e["gap"]))
		s.refreshActivity()
	case "review.done":
		s.setRunState("Ready", colorOK, false)
		ledger := list(e["ledger"])
		switch {
		case str(e["error"]) != "":
			s.finishProgress("review", noticeError, "Background review: "+str(e["error"]))
		case len(ledger) == 0:
			s.finishProgress("review", noticeInfo, "Background review: nothing worth saving.")
		default:
			s.finishProgress("review", noticeSuccess,
				fmt.Sprintf("Background review saved %d lessons.", len(ledger)))
		}
		s.refreshActivity()
	case "improve.auto":
		s.improveAuto = true
		s.notice(noticeInfo, "Auto-improve is armed: the harness is improved once this session goes idle.")
	case "improve.start":
		s.setRunState("Improving harness…", colorWarn, false)
		s.improveBusy(true)
		tag := ""
		if s.improveAuto {
			tag = " (auto)"
		}
		s.progress("improve", "Improving harness"+tag+" — drafting…")
	case "improve.propose":
		s.progress("improve", "Improving harness — drafting "+str(e["kind"])+" changes…")
	case "improve.probe":
		s.progress("improve", fmt.Sprintf("Improving harness — probing %s: %d steps in %ds",
			str(e["kind"]), int(num(e["steps"])), int(num(e["duration_s"]))))
	case "improve.compose":
		s.progress("improve", "Improving harness — composing the successor…")
	case "improve.done":
		s.setRunState("Ready", colorOK, false)
		s.improveBusy(false)
		s.improveAuto = false
		switch {
		case !boolean(e["ok"]):
			s.finishProgress("improve", noticeError, "Harness improvement failed: "+str(e["error"]))
		case !boolean(e["successor"]):
			s.finishProgress("improve", noticeInfo, "Harness improvement kept the current harness.")
		default:
			s.finishProgress("improve", noticeSuccess, "Harness improved — the successor applies to this session.")
		}
		s.refreshActivity()
	case "improve.library":
		if str(e["entry"]) != "" {
			s.notice(noticeSuccess, "Improvement library entry created: "+str(e["entry"]))
		} else if str(e["error"]) != "" {
			s.notice(noticeWarn, "Improvement library: "+str(e["error"]))
		}
		s.refreshActivity()
	case "improve.error":
		s.setRunState("Ready", colorWarn, false)
		s.improveBusy(false)
		s.improveAuto = false
		s.finishProgress("improve", noticeError, "Harness improvement: "+str(e["error"]))
		s.refreshActivity()
	case "memory.updated":
		s.notice(noticeSuccess, fmt.Sprintf("Memory %s: %s", str(e["target"]), str(e["action"])))
	case "memory.resolved":
		s.removeCard(str(e["id"]))
		if str(e["error"]) != "" {
			s.notice(noticeWarn, "Memory approval: "+str(e["error"]))
		} else if boolean(e["approved"]) {
			s.notice(noticeSuccess, "Memory saved.")
		}
	case "skill.applied":
		s.notice(noticeSuccess, fmt.Sprintf("Skill %s: %s", str(e["action"]), str(e["name"])))
	case "skill.resolved":
		s.removeCard(str(e["id"]))
		if str(e["error"]) != "" {
			s.notice(noticeWarn, "Skill approval: "+str(e["error"]))
		}
	case "harness.warning":
		s.notice(noticeWarn, "Harness: "+str(e["message"]))
	case "harness.warn":
		s.notice(noticeWarn, "Harness files changed — prose applies on the next run.")
		s.refreshActivity()
	case "harness.promoted":
		if str(e["error"]) != "" {
			s.notice(noticeWarn, "Harness promote: "+str(e["error"]))
		} else {
			s.notice(noticeSuccess, "Harness promoted (restart for guardrail rules).")
		}
		s.refreshActivity()
	case "config.changed":
		// Covers edits from the settings dialog, scripts (agent.saveConfig),
		// and theme/text-size changes; re-applying the theme is idempotent.
		applyTheme(s.app, s.a.Cfg.UI)
		if s.settingsForm != nil {
			s.settingsForm.onExternalChange(e)
		}
		switch {
		case str(e["error"]) != "":
			s.notice(noticeWarn, "Settings save: "+str(e["error"]))
		case e["text_scale"] != nil:
			s.notice(noticeInfo, fmt.Sprintf("Text size: %.0f%%", num(e["text_scale"])*100))
		default:
			s.notice(noticeInfo, "Configuration changed.")
		}
	case "skill.error":
		s.notice(noticeWarn, "Skill loader: "+str(e["error"]))
	case "tie.rebuilt":
		if str(e["error"]) != "" {
			s.notice(noticeWarn, "Index rebuild: "+str(e["error"]))
		} else {
			s.notice(noticeSuccess, fmt.Sprintf("Index rebuilt: %d sessions", int(num(e["sessions"]))))
		}
	}
}

func (s *shell) renderTodos(items []any) {
	s.todos.RemoveAll()
	for _, raw := range items {
		it := obj(raw)
		glyph := "☐"
		switch str(it["status"]) {
		case "in_progress":
			glyph = "▶"
		case "done":
			glyph = "☑"
		case "cancelled":
			glyph = "✖"
		}
		lbl := widget.NewLabel(glyph + " " + str(it["content"]))
		lbl.Wrapping = fyne.TextWrapWord
		lbl.Selectable = true
		s.todos.Add(lbl)
	}
	if s.todoPane != nil {
		if len(items) == 0 {
			s.todoPane.Hide()
		} else {
			s.todoPane.Show()
		}
		s.todoPane.Refresh()
		s.inspector.refresh()
	}
	s.todos.Refresh()
}

func memoryOpText(op map[string]any) string {
	switch str(op["action"]) {
	case "add":
		return "+ " + str(op["content"])
	case "patch":
		return shortText(str(op["old"]), 60) + "  →  " + shortText(str(op["new"]), 60)
	default:
		return "- " + shortText(str(op["old"]), 60)
	}
}

func shortText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func oneLine(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}

// normalizeEvent converts any event value (maps, review.Event structs, nested
// structs) into a JSON-shaped map so the UI has one consistent value model.
func normalizeEvent(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		// Core events are already maps, but nested values may be structs.
		b, err := json.Marshal(m)
		if err == nil {
			var out map[string]any
			if json.Unmarshal(b, &out) == nil {
				return out
			}
		}
		return m
	}
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

func boolean(v any) bool {
	b, _ := v.(bool)
	return b
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func stringList(v any) []string {
	items := list(v)
	if items == nil {
		if values, ok := v.([]string); ok {
			return values
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, str(item))
	}
	return out
}
