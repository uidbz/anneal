package main

import (
	"fmt"
	"strings"
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

// TestApprovalDockClearsCompletely: every resolved card must leave no
// residue. The old dock appended a spacer per card and only removed the card,
// so after the first approval the empty box stayed visible and later cards
// were pushed below the height cap.
func TestApprovalDockClearsCompletely(t *testing.T) {
	sh := newTestShell(t)
	for round := 0; round < 10; round++ {
		id := fmt.Sprintf("p%d", round)
		sh.handleEvent(normalizeEvent(map[string]any{
			"type": "perm.request", "id": id, "tool": "Bash", "message": "run?",
			"grant": "Bash(cmd)", "input": "cmd",
		}))
		if !sh.dock.visible || len(sh.dock.body.Objects) != 1 {
			t.Fatalf("round %d: visible=%v body=%d", round, sh.dock.visible, len(sh.dock.body.Objects))
		}
		sh.handleEvent(normalizeEvent(map[string]any{"type": "perm.resolved", "id": id}))
		if sh.dock.visible || len(sh.dock.body.Objects) != 0 {
			t.Fatalf("round %d: dock not cleared (visible=%v body=%d)",
				round, sh.dock.visible, len(sh.dock.body.Objects))
		}
	}
	if len(sh.pendingApprovals) != 0 || len(sh.permRows) != 0 {
		t.Fatalf("state leaked: pending=%d rows=%d", len(sh.pendingApprovals), len(sh.permRows))
	}
}

// TestApprovalDockShowsOldest: the card the agent is blocked on stays on
// screen, with a counter for the rest, instead of stacking downward.
func TestApprovalDockShowsOldest(t *testing.T) {
	sh := newTestShell(t)
	for _, id := range []string{"a", "b", "c"} {
		sh.handleEvent(normalizeEvent(map[string]any{
			"type": "perm.request", "id": id, "tool": "Bash", "message": id,
		}))
	}
	if got := len(sh.dock.body.Objects); got != 1 {
		t.Fatalf("dock shows %d cards, want 1", got)
	}
	if sh.dock.active() != sh.permRows["a"] {
		t.Fatal("active card is not the oldest")
	}
	if !sh.dock.header.Visible() || !strings.Contains(sh.dock.counter.Text(), "3 approvals") {
		t.Fatalf("header/counter missing: %q", sh.dock.counter.Text())
	}
	sh.dock.selectIdx(1)
	if sh.dock.active() != sh.permRows["b"] {
		t.Fatal("paging did not select the next card")
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "perm.resolved", "id": "a"}))
	if sh.dock.visible != true || len(sh.dock.body.Objects) != 1 {
		t.Fatal("resolving a background card disturbed the active one")
	}
}

// TestApprovalCardReplayIsIdempotent: a reconnect replay re-sends pending
// cards; they must not be docked twice.
func TestApprovalCardReplayIsIdempotent(t *testing.T) {
	sh := newTestShell(t)
	ev := normalizeEvent(map[string]any{
		"type": "perm.request", "id": "p1", "tool": "Bash", "message": "run?",
	})
	sh.handleEvent(ev)
	sh.handleEvent(ev)
	if len(sh.pendingApprovals) != 1 || len(sh.permRows) != 1 {
		t.Fatalf("duplicate card: pending=%d rows=%d",
			len(sh.pendingApprovals), len(sh.permRows))
	}
}

// TestQueueTrayFooters: a message sent while a run is active is visible as
// queued, picked up, and stopped — never silently dropped.
func TestQueueTrayFooters(t *testing.T) {
	sh := newTestShell(t)
	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "input.queued", "position": 1,
		"input": map[string]any{"id": "i1", "text": "do the thing", "ref": "c1"},
	}))
	if len(sh.queued) != 1 || !sh.queueTray.visible {
		t.Fatalf("queued=%d tray=%v", len(sh.queued), sh.queueTray.visible)
	}
	footer := sh.userFooters["c1"]
	if footer == nil || !strings.Contains(footer.Text(), "queued") {
		t.Fatalf("footer = %v", footer)
	}
	sh.handleEvent(normalizeEvent(map[string]any{
		"type":  "input.dequeued",
		"input": map[string]any{"id": "i1", "text": "do the thing", "ref": "c1"},
	}))
	if len(sh.queued) != 0 || sh.queueTray.visible {
		t.Fatal("tray not cleared after dequeue")
	}
	if !strings.Contains(footer.Text(), "picked up") {
		t.Fatalf("footer after dequeue = %q", footer.Text())
	}
	// run.started for the same ref must not render a second user turn.
	blocks := len(sh.visible)
	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "run.started", "run": 1, "ref": "c1", "text": "do the thing",
	}))
	if len(sh.visible) != blocks {
		t.Fatalf("run.started duplicated the user turn (%d -> %d)", blocks, len(sh.visible))
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "run.finished", "run": 1, "ok": true}))
	if !strings.Contains(footer.Text(), "done") {
		t.Fatalf("footer after finish = %q", footer.Text())
	}
}

// TestRunStartedFromOtherClientRendersTurn: in multi-client mode every client
// shows the user turn, not just the sender.
func TestRunStartedFromOtherClientRendersTurn(t *testing.T) {
	sh := newTestShell(t)
	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "run.started", "run": 2, "ref": "other", "text": "from another client",
	}))
	if sh.turn != turnUser || len(sh.visible) != 1 {
		t.Fatalf("turn=%v blocks=%d", sh.turn, len(sh.visible))
	}
}

// TestQueuePausedAfterStop: a stop pauses the queue instead of auto-sending.
func TestQueuePausedAfterStop(t *testing.T) {
	sh := newTestShell(t)
	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "input.queued", "position": 1,
		"input": map[string]any{"id": "i1", "text": "next", "ref": "c1"},
	}))
	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "queue.paused", "reason": "previous run stopped", "count": 1,
	}))
	if !sh.queuePaused || !sh.queueTray.resume.Visible() {
		t.Fatal("paused queue does not offer Run next")
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "queue.resumed", "count": 1}))
	if sh.queuePaused || sh.queueTray.resume.Visible() {
		t.Fatal("resume did not clear the pause")
	}
}

func TestComposerApprovalShortcuts(t *testing.T) {
	e := newComposerEntry()
	once, always, rejected := 0, 0, 0
	e.onCtrlCR = func() { once++ }
	e.onCtrlSh = func() { always++ }
	e.onEscape = func() { rejected++ }

	e.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyReturn, Modifier: fyne.KeyModifierControl})
	e.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyReturn,
		Modifier: fyne.KeyModifierControl | fyne.KeyModifierShift})
	e.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
	if once != 1 || always != 1 || rejected != 1 {
		t.Fatalf("once=%d always=%d reject=%d", once, always, rejected)
	}
	// With text in the composer, Esc stays a normal editor key.
	e.SetText("x")
	e.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
	if rejected != 1 {
		t.Fatalf("Esc rejected an approval while typing (rejected=%d)", rejected)
	}
}
