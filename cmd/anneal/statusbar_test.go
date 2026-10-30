package main

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

func TestStatusChipsFollowState(t *testing.T) {
	sh := newTestShell(t)
	sh.handleEvent(normalizeEvent(map[string]any{"type": "provider.state",
		"health": map[string]any{"state": "online", "model": "qwen3-coder", "ctx_window": 262144}}))
	if got := sh.modelChip.Text(); got != "qwen3-coder" {
		t.Fatalf("model chip = %q", got)
	}
	if sh.ctxWindow != 262144 || sh.modelChip.dot.token != colorOK {
		t.Fatalf("ctxWindow=%d token=%v", sh.ctxWindow, sh.modelChip.dot.token)
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "provider.state",
		"health": map[string]any{"state": "offline", "error": "connection refused"}}))
	if got := sh.modelChip.Text(); !strings.Contains(got, "connection refused") {
		t.Fatalf("offline chip = %q", got)
	}
	if sh.modelChip.dot.token != colorErr {
		t.Fatalf("offline dot = %v", sh.modelChip.dot.token)
	}

	sh.handleEvent(normalizeEvent(map[string]any{"type": "tie.state",
		"state": map[string]any{"enabled": true, "online": true, "collection": "anneal"}}))
	if !sh.tieChip.Visible() || sh.tieChip.Text() != "index anneal" {
		t.Fatalf("tie chip = %q visible=%v", sh.tieChip.Text(), sh.tieChip.Visible())
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "mcp.state", "servers": []any{
		map[string]any{"name": "a", "connected": true, "tools": 3},
		map[string]any{"name": "b", "connected": true, "tools": 2},
	}}))
	if got := sh.mcpChip.Text(); got != "MCP 2/2 · 5 tools" {
		t.Fatalf("mcp chip = %q", got)
	}
}

func TestRunClockAndUsage(t *testing.T) {
	sh := newTestShell(t)
	sh.handleEvent(normalizeEvent(map[string]any{"type": "run.started", "run": 1}))
	if sh.elapsed.Text() == "" {
		t.Fatal("elapsed clock did not start")
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "run.finished", "run": 1, "ok": true,
		"usage": map[string]any{"tin": 12000, "tout": 3000}, "ctx_pct": 42}))
	if sh.elapsed.Text() != "" {
		t.Fatalf("elapsed after finish = %q", sh.elapsed.Text())
	}
	if got := sh.usage.Text(); got != "↑ 12k ↓ 3k · ctx 42%" {
		t.Fatalf("usage = %q", got)
	}
}

func TestMeterThresholds(t *testing.T) {
	cases := map[float64]fyne.ThemeColorName{
		0:    colorBorder,
		0.1:  "primary",
		0.70: colorWarn,
		0.82: colorErr,
		0.99: colorErr,
	}
	for v, want := range cases {
		if got := meterToken(v); got != want {
			t.Errorf("meterToken(%v) = %v, want %v", v, got, want)
		}
	}
	if got := meterToken(0.5); got != theme.ColorNamePrimary {
		t.Errorf("meterToken(0.5) = %v", got)
	}
}

func TestInspectorContext(t *testing.T) {
	sh := newTestShell(t)
	sh.renderTodos([]any{
		map[string]any{"content": "one", "status": "done"},
		map[string]any{"content": "two", "status": "in_progress"},
	})
	if got := sh.inspector.taskCount.text; got != "2" {
		t.Fatalf("task count = %q", got)
	}
	sh.ctxWindow = 262144
	sh.inspector.ctxPct = 42
	sh.inspector.tin, sh.inspector.tout = 12000, 3000
	sh.inspector.compacted = 1
	sh.inspector.refresh()
	if got := sh.inspector.ctxLabel.Text(); got != "42% of 262k tokens" {
		t.Fatalf("ctx label = %q", got)
	}
	if got := sh.inspector.ctxTokens.Text(); got != "↑ 12k in · ↓ 3k out" {
		t.Fatalf("ctx tokens = %q", got)
	}
	if got := sh.inspector.ctxComp.Text(); got == "" {
		t.Fatal("compaction count missing")
	}
	if got := sh.inspector.harness.Text(); got != "Harness: base" {
		t.Fatalf("harness label = %q", got)
	}
}
