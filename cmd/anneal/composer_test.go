package main

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

func TestComposerEntryEnterSendsShiftNewline(t *testing.T) {
	e := newComposerEntry()
	sent := 0
	e.onSend = func() { sent++ }

	e.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	if sent != 1 {
		t.Fatalf("Enter did not send (sent=%d)", sent)
	}

	e.KeyDown(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
	e.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	e.KeyUp(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
	if sent != 1 {
		t.Fatalf("Shift+Enter sent a message (sent=%d)", sent)
	}
	if e.Text != "\n" {
		t.Fatalf("Shift+Enter text = %q, want newline", e.Text)
	}

	// The shift state must not stick after key-up.
	e.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	if sent != 2 {
		t.Fatalf("Enter after Shift+Enter did not send (sent=%d)", sent)
	}
}

func TestComposerCtrlEnterCallback(t *testing.T) {
	e := newComposerEntry()
	allowed := 0
	e.onCtrlCR = func() { allowed++ }
	e.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyReturn, Modifier: fyne.KeyModifierControl})
	if allowed != 1 {
		t.Fatalf("Ctrl+Enter callback ran %d times", allowed)
	}
}

func TestComposerRowsGrow(t *testing.T) {
	e := newComposerEntry()
	if e.minRows != 2 {
		t.Fatalf("initial rows = %d", e.minRows)
	}
	e.SetText("a\nb\nc\nd")
	if e.minRows != 4 {
		t.Fatalf("rows after 4 lines = %d", e.minRows)
	}
	e.SetText("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk")
	if e.minRows != 8 {
		t.Fatalf("rows capped at %d", e.minRows)
	}
}

func TestSendStopButtonFollowsRunState(t *testing.T) {
	sh := newTestShell(t)
	if sh.cmp.actionBtn.Text != "Send" {
		t.Fatalf("idle action = %q", sh.cmp.actionBtn.Text)
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "run.started", "run": 1}))
	if sh.cmp.actionBtn.Text != "Stop" || !sh.cmp.running {
		t.Fatalf("running action = %q", sh.cmp.actionBtn.Text)
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "run.finished", "run": 1, "ok": true,
		"usage": map[string]any{"tin": 1, "tout": 1}}))
	if sh.cmp.actionBtn.Text != "Send" || sh.cmp.running {
		t.Fatalf("finished action = %q", sh.cmp.actionBtn.Text)
	}
}

func TestComposerModeSwitch(t *testing.T) {
	sh := newTestShell(t)
	sh.handleEvent(normalizeEvent(map[string]any{"type": "mode.changed", "mode": "plan"}))
	if sh.cmp.planBtn.Importance != widget.HighImportance {
		t.Fatal("plan button not highlighted")
	}
	if sh.cmp.buildBtn.Importance != widget.LowImportance {
		t.Fatal("build button still highlighted in plan mode")
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "mode.changed", "mode": "default"}))
	if sh.cmp.buildBtn.Importance != widget.HighImportance {
		t.Fatal("build button not highlighted")
	}
}

func TestApprovalDockLifecycle(t *testing.T) {
	sh := newTestShell(t)
	if sh.dock.visible {
		t.Fatal("dock visible with no approvals")
	}
	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "perm.request", "id": "p1", "tool": "Bash", "message": "run?",
		"grant": "Bash(cmd)", "input": "cmd",
	}))
	if !sh.dock.visible || len(sh.permRows) != 1 || len(sh.pendingApprovals) != 1 {
		t.Fatalf("dock=%v rows=%d pending=%d", sh.dock.visible, len(sh.permRows), len(sh.pendingApprovals))
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "perm.resolved", "id": "p1", "reason": "allow"}))
	if sh.dock.visible || len(sh.permRows) != 0 || len(sh.pendingApprovals) != 0 {
		t.Fatalf("dock not cleared: visible=%v rows=%d", sh.dock.visible, len(sh.permRows))
	}
}

func TestAllowOldestApproval(t *testing.T) {
	sh := newTestShell(t)
	var order []string
	for _, id := range []string{"a", "b"} {
		id := id
		sh.addApprovalCard(pendingApproval{
			id: id, kind: "permission",
			allow: func() { order = append(order, id) },
		}, "Permission required", "m", "", "",
			widget.NewButton("Allow once", func() {}))
	}
	sh.allowOldest()
	if len(order) != 1 || order[0] != "a" {
		t.Fatalf("allowOldest order = %v", order)
	}
}

func TestComposerEntryPlaceholder(t *testing.T) {
	sh := newTestShell(t)
	if sh.entry.PlaceHolder == "" {
		t.Fatal("composer has no placeholder")
	}
	if !sh.entry.MultiLine {
		t.Fatal("composer is not multi-line")
	}
}
