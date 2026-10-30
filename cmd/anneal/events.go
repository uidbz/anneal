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
		s.addTool(fmt.Sprintf("🔧 %s %s", str(e["name"]), oneLine(str(e["input"]))))
	case "tool.result":
		mark := "✔"
		if boolean(e["error"]) {
			mark = "✖"
		}
		s.addTool(fmt.Sprintf("  %s %s: %s", mark, str(e["name"]), oneLine(str(e["preview"]))))
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
		s.addNotice(fmt.Sprintf("⚠ session %s: %s", str(e["id"]), str(e["error"])))
	case "mode.changed":
		s.mode = str(e["mode"])
		s.modeBtn.SetText(modeTitle(s.mode))
		text, fill, fg := modeBadge(s.mode)
		s.header.setBadge(text, fill, fg)
		s.inspector.refresh()
	case "todo.updated":
		s.renderTodos(list(e["items"]))
	case "perm.request":
		s.addPerm(e)
	case "perm.saved":
		s.addNotice(fmt.Sprintf("🔐 saved permission: %s %s", str(e["action"]),
			strings.Join(stringList(e["resources"]), ", ")))
	case "perm.warning":
		s.addNotice("⚠ permission save failed: " + str(e["error"]))
	case "perm.resolved":
		s.removeCard(str(e["id"]))
	case "memory.stage":
		op := obj(e["op"])
		id := str(op["id"])
		info := fmt.Sprintf("💾 memory[%s] %s via %s\n%s", str(op["target"]),
			str(op["action"]), str(op["source"]), memoryOpText(op))
		s.addApprovalCard(id, info,
			widget.NewButton("approve", func() { s.a.RespondMemory(id, "approve") }),
			widget.NewButton("reject", func() { s.a.RespondMemory(id, "reject") }),
		)
	case "skill.stage":
		op := obj(e["op"])
		id := str(op["id"])
		info := fmt.Sprintf("🛠 skill[%s] %s via %s\n%s", str(op["action"]),
			str(op["name"]), str(op["source"]), str(e["preview"]))
		s.addApprovalCard(id, info,
			widget.NewButton("approve", func() { s.a.RespondSkill(id, "approve") }),
			widget.NewButton("reject", func() { s.a.RespondSkill(id, "reject") }),
		)
	case "harness.overlay":
		s.inspector.refresh()
	case "compaction.done":
		s.inspector.compacted++
		s.addNotice(fmt.Sprintf("🗜 context compacted: %d → %d tokens (%d msgs)",
			int(num(e["before"])), int(num(e["after"])), int(num(e["msgs"]))))
	case "review.start":
		s.setRunState("reviewing…", colorWarn, false)
		s.addNotice("🧠 background review started")
	case "review.skipped":
		s.setRunState("ready", colorOK, false)
		if str(e["reason"]) != "preempted by user" {
			s.addNotice("🧠 review skipped: " + str(e["reason"]))
		}
	case "review.flag":
		s.addNotice("🪧 reviewer flagged a harness gap: " + str(e["gap"]))
		s.refreshActivity()
	case "review.done":
		s.setRunState("ready", colorOK, false)
		ledger := list(e["ledger"])
		switch {
		case str(e["error"]) != "":
			s.addNotice("⚠ background review: " + str(e["error"]))
		case len(ledger) == 0:
			s.addNotice("🧠 review: nothing worth saving")
		default:
			s.addNotice(fmt.Sprintf("🧠 review saved %d lessons", len(ledger)))
		}
		s.refreshActivity()
	case "improve.auto":
		s.improveAuto = true
		s.addNotice("⚡ auto-improve: idle after a successful run — improving the harness once for this session")
	case "improve.start":
		s.setRunState("improving harness…", colorWarn, false)
		s.improveBtn.SetText("improving…")
		tag := ""
		if s.improveAuto {
			tag = " (auto)"
		}
		s.addNotice("🔧 harness improvement started" + tag + " (run " + str(e["run"]) + ")")
	case "improve.propose":
		s.addNotice("🔧 propose: " + str(e["kind"]) + " editor drafting…")
	case "improve.probe":
		s.addNotice(fmt.Sprintf("🔧 probe %s: %d steps in %ds",
			str(e["kind"]), int(num(e["steps"])), int(num(e["duration_s"]))))
	case "improve.compose":
		s.addNotice("🔧 composed successor")
	case "improve.done":
		s.setRunState("ready", colorOK, false)
		s.improveBtn.SetText("improve")
		s.improveAuto = false
		switch {
		case !boolean(e["ok"]):
			s.addNotice("⚠ improve: " + str(e["error"]))
		case !boolean(e["successor"]):
			s.addNotice("🔧 improve: composer kept the current harness")
		default:
			s.addNotice("🔧 improve: successor applied to this session")
		}
		s.refreshActivity()
	case "improve.library":
		if str(e["entry"]) != "" {
			s.addNotice("🔧 improvement library entry: " + str(e["entry"]))
		} else if str(e["error"]) != "" {
			s.addNotice("⚠ improvement library: " + str(e["error"]))
		}
		s.refreshActivity()
	case "improve.error":
		s.setRunState("ready", colorWarn, false)
		s.improveBtn.SetText("improve")
		s.improveAuto = false
		s.addNotice("⚠ improve: " + str(e["error"]))
		s.refreshActivity()
	case "memory.updated":
		s.addNotice(fmt.Sprintf("💾 memory %s: %s", str(e["target"]), str(e["action"])))
	case "memory.resolved":
		s.removeCard(str(e["id"]))
		if str(e["error"]) != "" {
			s.addNotice("⚠ memory approval: " + str(e["error"]))
		} else if boolean(e["approved"]) {
			s.addNotice("✔ memory saved")
		}
	case "skill.applied":
		s.addNotice(fmt.Sprintf("✔ skill %s: %s", str(e["action"]), str(e["name"])))
	case "skill.resolved":
		s.removeCard(str(e["id"]))
		if str(e["error"]) != "" {
			s.addNotice("⚠ skill approval: " + str(e["error"]))
		}
	case "harness.warning":
		s.addNotice("⚠ harness: " + str(e["message"]))
	case "harness.warn":
		s.addNotice("⚠ harness files changed — prose applies next run")
		s.refreshActivity()
	case "harness.promoted":
		if str(e["error"]) != "" {
			s.addNotice("⚠ harness promote: " + str(e["error"]))
		} else {
			s.addNotice("✔ harness promoted (restart for guardrail rules)")
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
			s.addNotice("⚠ settings save: " + str(e["error"]))
		case e["text_scale"] != nil:
			s.addNotice(fmt.Sprintf("⚙ text size: %.0f%%", num(e["text_scale"])*100))
		default:
			s.addNotice("⚙ configuration changed")
		}
	case "skill.error":
		s.addNotice("⚠ skill loader: " + str(e["error"]))
	case "tie.rebuilt":
		if str(e["error"]) != "" {
			s.addNotice("⚠ index rebuild: " + str(e["error"]))
		} else {
			s.addNotice(fmt.Sprintf("✔ index rebuilt: %d sessions", int(num(e["sessions"]))))
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
