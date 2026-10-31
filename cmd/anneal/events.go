package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
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
			ctx := int(num(h["ctx_window"]))
			s.ctxWindow = ctx
			s.modelChip.SetText(trunc(str(h["model"]), 22))
			s.modelChip.dot.setToken(colorOK)
			s.cmp.setModel(str(h["model"]), ctx)
			s.inspector.refresh()
		case "connecting", "probing":
			s.modelChip.SetText(state + "…")
			s.modelChip.dot.setToken(colorWarn)
		default:
			msg := str(h["error"])
			text := state
			if msg != "" {
				text += " — " + trunc(msg, 40)
			}
			s.modelChip.SetText(text)
			s.modelChip.dot.setToken(colorErr)
		}
		s.modelChip.Show()
	case "tie.state":
		st := obj(e["state"])
		switch {
		case !boolean(st["enabled"]):
			s.tieShown = false
			s.tieChip.Hide()
		case boolean(st["online"]):
			s.tieChip.SetText("index " + str(st["collection"]))
			s.tieChip.dot.setToken(colorOK)
			s.tieShown = true
			s.tieChip.Show()
		default:
			s.tieChip.SetText("index offline")
			s.tieChip.dot.setToken(colorErr)
			s.tieShown = true
			s.tieChip.Show()
		}
	case "mcp.state":
		servers := list(e["servers"])
		s.mcpServers = nil
		for _, raw := range servers {
			s.mcpServers = append(s.mcpServers, obj(raw))
		}
		up, tools := 0, 0
		for _, raw := range servers {
			srv := obj(raw)
			if boolean(srv["connected"]) {
				up++
			}
			tools += int(num(srv["tools"]))
		}
		if len(servers) > 0 {
			s.mcpChip.SetText(fmt.Sprintf("MCP %d/%d · %d tools", up, len(servers), tools))
			switch {
			case up == len(servers):
				s.mcpChip.dot.setToken(colorOK)
			case up > 0:
				s.mcpChip.dot.setToken(colorWarn)
			default:
				s.mcpChip.dot.setToken(colorErr)
			}
			s.mcpShown = true
			s.mcpChip.Show()
		} else {
			s.mcpShown = false
			s.mcpChip.Hide()
		}
	case "run.started":
		ref, text := str(e["ref"]), str(e["text"])
		if run := int(num(e["run"])); run > 0 && ref != "" {
			s.runRefs[run] = ref
		}
		if ref != "" {
			if footer := s.userFooters[ref]; footer != nil {
				footer.SetText("picked up")
			} else if text != "" {
				// Started by another client: every client renders the turn.
				s.addUser(text)
			}
		} else if text != "" {
			s.addUser(text)
		}
		s.setRunState("Running…", colorWarn, false)
		s.setUsageText(0, 0, 0)
		s.cmp.setRunning(true)
		s.startRunClock()
	case "run.stopping":
		if str(e["mode"]) == "hard" {
			s.setRunState("Stopping now…", colorWarn, false)
		} else {
			s.setRunState("Stopping after this step… (Ctrl+D again to force)", colorWarn, false)
		}
	case "turn.budget":
		if str(e["phase"]) == "exhausted" {
			s.notice(noticeWarn, fmt.Sprintf("Turn budget exhausted (%ds of %ds) — finishing with a hand-off summary",
				int(num(e["used_s"])), int(num(e["budget_s"]))))
		} else {
			s.notice(noticeInfo, fmt.Sprintf("Turn budget: %ds of %ds used — checkpointing",
				int(num(e["used_s"])), int(num(e["budget_s"]))))
		}
	case "gortex.untracked":
		if !s.gortexNotice {
			s.gortexNotice = true
			text := fmt.Sprintf("gortex does not track %s, so its navigation tools are "+
				"off for this project (they would fail with \"repository not tracked\"). "+
				"Tracked repositories: %s.", str(e["cwd"]),
				strings.Join(stringList(e["tracked"]), ", "))
			s.appendBlock(s.actionBlock("Code intelligence off", text,
				"Track this project", s.trackGortex))
		}
	case "gortex.tracked":
		s.gortexNotice = false
		s.notice(noticeSuccess, "gortex now tracks "+str(e["cwd"])+
			"; its tools are available from the next run.")
	case "tool.recovered":
		s.notice(noticeWarn, "Cancelled "+str(e["tool"])+
			" input saved: "+str(e["path"])+" (ask the agent to re-apply it instead of regenerating)")
	case "model.wait":
		s.setRunState(fmt.Sprintf("Waiting for model… (~%s context)", humanK(int(num(e["input_est"])))), colorWarn, false)
	case "model.first_token":
		ttft := time.Duration(num(e["ttft_ms"])) * time.Millisecond
		s.setRunState(fmt.Sprintf("Writing… (first token after %s)",
			formatElapsed(ttft)), colorWarn, false)
		if ref := s.runRefs[int(num(e["run"]))]; ref != "" {
			if footer := s.userFooters[ref]; footer != nil {
				footer.SetText("picked up · first token after " + formatElapsed(ttft))
			}
		}
	case "run.stats":
		s.lastStats = e
		s.inspector.refresh()
	case "run.busy":
		s.notice(noticeWarn, "A run is already active — the message was not sent.")
	case "text.delta":
		s.ensureTail()
		s.tailBuf += str(e["text"])
		s.flushTail()
	case "thinking.delta":
		s.status.SetText("Thinking…")
		s.runDot.setToken(colorWarn)
	case "tool.composing":
		s.progress("compose", fmt.Sprintf("Composing %s… %s",
			toolDisplayName(str(e["name"])), humanBytes(num(e["bytes"]))))
	case "message.final":
		s.dropProgress("compose")
		s.closeTail(str(e["text"]))
	case "tool.start":
		s.dropProgress("compose")
		s.closeTail("")
		s.addTool(str(e["id"]), str(e["name"]), str(e["input"]))
		s.setRunState("Running "+toolDisplayName(str(e["name"]))+"…", colorWarn, false)
	case "tool.result":
		s.finishTool(str(e["id"]), str(e["name"]), str(e["preview"]), boolean(e["error"]))
		s.updateApprovalStatus()
	case "run.finished":
		s.closeTail("")
		u := obj(e["usage"])
		tin, tout := int(num(u["tin"])), int(num(u["tout"]))
		pct := int(num(e["ctx_pct"]))
		s.setUsageText(tin, tout, pct)
		s.stopRunClock()
		s.inspector.tin = tin
		s.inspector.tout = tout
		s.inspector.ctxPct = pct
		s.inspector.refresh()
		s.refreshSessionMeta()
		switch {
		case boolean(e["stopped"]):
			s.setRunState("Stopped", colorWarn, false)
		case boolean(e["ok"]):
			s.setRunState("Ready", colorOK, false)
		default:
			s.setRunState("Error", colorErr, true)
			if msg := str(e["error"]); msg != "" {
				s.addNotice("⚠ " + msg)
			}
		}
		if outcome, ok := e["outcome"].(map[string]any); ok {
			reason := str(outcome["reason"])
			switch reason {
			case "budget", "deadline":
				s.setRunState("Time limit reached", colorWarn, true)
			case "iteration_limit":
				s.setRunState("Step limit reached", colorWarn, true)
			}
			// A user stop already has its own "Stopped" status; every other
			// interruption gets an explanation with Continue/Details.
			if reason != "canceled" && str(outcome["summary"]) != "" {
				s.appendBlock(s.outcomeBlock(outcome))
			}
		}
		s.cmp.setRunning(false)
		if ref := s.runRefs[int(num(e["run"]))]; ref != "" {
			if footer := s.userFooters[ref]; footer != nil {
				switch {
				case boolean(e["stopped"]):
					footer.SetText("stopped before the answer")
				case boolean(e["ok"]):
					footer.SetText("✓ done")
				default:
					footer.SetText("✗ failed" + errorSuffix(str(e["error"])))
				}
			}
			delete(s.runRefs, int(num(e["run"])))
		}
		s.scroll.ScrollToBottom()
		if s.autoQuit {
			time.AfterFunc(1500*time.Millisecond, func() { fyne.Do(func() { s.app.Quit() }) })
		}
	case "input.queued":
		in := obj(e["input"])
		id := str(in["id"])
		if id != "" && s.queuedIndex(id) < 0 {
			s.queued = append(s.queued, queuedItem{ID: id, Text: str(in["text"]), Ref: str(in["ref"])})
		}
		s.queueTray.set(s.queued, s.queuePaused, s.queueReason)
		ref := str(in["ref"])
		pos := int(num(e["position"]))
		if ref != "" {
			if footer := s.userFooters[ref]; footer != nil {
				footer.SetText(fmt.Sprintf("queued · %d", pos))
			} else if text := str(in["text"]); text != "" {
				s.addUserPending(text, ref)
				s.setQueuedFooters(ref, fmt.Sprintf("queued · %d", pos))
			}
		}
	case "input.dequeued":
		in := obj(e["input"])
		if i := s.queuedIndex(str(in["id"])); i >= 0 {
			s.queued = append(s.queued[:i], s.queued[i+1:]...)
		}
		s.queueTray.set(s.queued, s.queuePaused, s.queueReason)
		s.setQueuedFooters(str(in["ref"]), "picked up · running")
	case "input.cancelled":
		in := obj(e["input"])
		if i := s.queuedIndex(str(in["id"])); i >= 0 {
			s.queued = append(s.queued[:i], s.queued[i+1:]...)
		}
		s.queueTray.set(s.queued, s.queuePaused, s.queueReason)
		s.setQueuedFooters(str(in["ref"]), "cancelled — not sent")
	case "input.failed":
		ref := str(e["ref"])
		if footer := s.userFooters[ref]; footer != nil {
			footer.token = colorErr
			footer.SetText("not sent: " + str(e["error"]))
			footer.Refresh()
		}
		s.notice(noticeWarn, "Message not sent: "+str(e["error"]))
	case "queue.paused":
		s.queuePaused, s.queueReason = true, str(e["reason"])
		s.queueTray.set(s.queued, s.queuePaused, s.queueReason)
		s.notice(noticeWarn, fmt.Sprintf("Queue paused (%s): %d message(s) waiting — "+
			"Run next or Clear in the queue tray", s.queueReason, int(num(e["count"]))))
	case "queue.resumed":
		s.queuePaused, s.queueReason = false, ""
		s.queueTray.set(s.queued, false, "")
	case "queue.promoted":
		in := obj(e["input"])
		id := str(in["id"])
		if i := s.queuedIndex(id); i > 0 {
			item := s.queued[i]
			s.queued = append(s.queued[:i], s.queued[i+1:]...)
			s.queued = append([]queuedItem{item}, s.queued...)
		}
		s.queueTray.set(s.queued, s.queuePaused, s.queueReason)
	case "queue.cleared":
		var texts []string
		for _, raw := range list(e["inputs"]) {
			in := obj(raw)
			s.setQueuedFooters(str(in["ref"]), "cleared — not sent")
			if text := strings.TrimSpace(str(in["text"])); text != "" {
				texts = append(texts, text)
			}
		}
		if len(texts) > 0 && strings.TrimSpace(s.entry.Text) == "" {
			s.entry.SetText(strings.Join(texts, "\n"))
		}
		s.queued = nil
		s.queuePaused, s.queueReason = false, ""
		s.queueTray.set(nil, false, "")
	case "queue.state":
		s.queued = nil
		for _, raw := range list(e["items"]) {
			in := obj(raw)
			s.queued = append(s.queued, queuedItem{
				ID: str(in["id"]), Text: str(in["text"]), Ref: str(in["ref"]),
			})
		}
		s.queuePaused, s.queueReason = boolean(e["paused"]), str(e["reason"])
		s.queueTray.set(s.queued, s.queuePaused, s.queueReason)
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
		since := int(num(e["calls_since"]))
		switch {
		case len(list(e["items"])) == 0:
			s.inspector.setTodoMeta("", false)
		case boolean(e["stale"]):
			s.inspector.setTodoMeta(fmt.Sprintf("⚠ may be stale · %d tool calls since update", since), true)
		case since > 0:
			s.inspector.setTodoMeta(fmt.Sprintf("last update %d tool calls ago", since), false)
		default:
			s.inspector.setTodoMeta("updated just now", false)
		}
	case "todo.stale":
		s.inspector.setTodoMeta(fmt.Sprintf("⚠ may be stale · %d tool calls since update (%dm)",
			int(num(e["calls_since"])), int(num(e["age_s"]))/60), true)
	case "perm.request":
		s.addPermEvent(e)
	case "perm.saved":
		scope := "for this project"
		if str(e["scope"]) == "session" {
			scope = "for this session"
		}
		s.notice(noticeSuccess, fmt.Sprintf("Permission saved %s: %s %s", scope,
			str(e["action"]), strings.Join(stringList(e["resources"]), ", ")))
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
	case "improve.preempted":
		s.improveBusy(false)
		s.improveAuto = false
		s.notice(noticeInfo, "Harness improvement paused (you started working); the next run re-arms it.")
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
		// Local mode: the core's config is authoritative (shared file), so
		// point the client copy at it. Remote: s.cfg stays client-only.
		if !s.b.remote() && s.a != nil {
			s.cfg = s.a.Cfg
		}
		applyTheme(s.app, s.cfg.UI)
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
	case "backend.disconnected":
		s.setRunState("Backend offline", colorErr, true)
		if s.backendChip != nil {
			s.backendChip.dot.setToken(colorErr)
			s.backendChip.Refresh()
		}
		s.addNotice("⚠ disconnected from backend " + str(e["backend"]) +
			" — restart anneal to reconnect")
	case "server.shutting_down":
		s.notice(noticeWarn, "Backend shutting down ("+str(e["reason"])+")")
	case "tie.rebuilt":
		if str(e["error"]) != "" {
			s.notice(noticeWarn, "Index rebuild: "+str(e["error"]))
		} else {
			s.notice(noticeSuccess, fmt.Sprintf("Index rebuilt: %d sessions", int(num(e["sessions"]))))
		}
	}
}

// errorSuffix renders a short " — reason" for a delivery footer.
func errorSuffix(msg string) string {
	if strings.TrimSpace(msg) == "" {
		return ""
	}
	return " — " + trunc(msg, 80)
}

func (s *shell) renderTodos(items []any) {
	s.todos.RemoveAll()
	for _, raw := range items {
		it := obj(raw)
		glyph, token := "☐", colorMuted
		status := str(it["status"])
		switch status {
		case "in_progress":
			glyph, token = "▶", theme.ColorNamePrimary
		case "done":
			glyph, token = "☑", colorOK
		case "cancelled":
			glyph, token = "✖", colorErr
		}
		s.todos.Add(newTaskRow(glyph, token, str(it["content"]), status))
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
// Hot streaming events already carry only plain values; skipping the JSON
// round-trip for them keeps per-token overhead out of the event path.
func normalizeEvent(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		switch m["type"] {
		case "text.delta", "thinking.delta", "tool.composing", "model.wait",
			"model.first_token", "tool.start", "tool.result", "run.started", "run.busy":
			return m
		}
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
