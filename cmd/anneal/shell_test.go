package main

import (
	"fmt"
	"testing"

	"fyne.io/fyne/v2/test"

	coreapp "github.com/uidbz/anneal/core/app"
)

func newTestShell(t *testing.T) *shell {
	t.Helper()
	t.Setenv("ANNEAL_HOME", t.TempDir())
	a, err := coreapp.New()
	if err != nil {
		t.Fatal(err)
	}
	fyneApp := test.NewApp()
	t.Cleanup(fyneApp.Quit)
	win := fyneApp.NewWindow("test")
	src, err := paneSource("")
	if err != nil {
		t.Fatal(err)
	}
	sh, err := newShell(a, fyneApp, win, src)
	if err != nil {
		t.Fatal(err)
	}
	win.SetContent(sh.root)
	return sh
}

func TestTranscriptWindowAndEarlier(t *testing.T) {
	sh := newTestShell(t)
	sh.maxVisible = 2

	for i := 0; i < 5; i++ {
		sh.addMarkdown(fmt.Sprintf("block %d", i))
	}
	if len(sh.visible) != 2 || len(sh.hidden) != 3 {
		t.Fatalf("window: visible=%d hidden=%d", len(sh.visible), len(sh.hidden))
	}
	if !sh.earlier.Visible() {
		t.Fatal("earlier button hidden while blocks are hidden")
	}

	sh.showEarlier() // restore the 2 newest hidden blocks
	if len(sh.visible) != 4 || len(sh.hidden) != 1 {
		t.Fatalf("restore 1: visible=%d hidden=%d", len(sh.visible), len(sh.hidden))
	}
	sh.showEarlier() // restore the last one
	if len(sh.visible) != 5 || len(sh.hidden) != 0 {
		t.Fatalf("restore 2: visible=%d hidden=%d", len(sh.visible), len(sh.hidden))
	}
	if sh.earlier.Visible() {
		t.Fatal("earlier button still visible with no hidden blocks")
	}
	if got := len(sh.chatBox.Objects); got != 5 {
		t.Fatalf("chat box objects: %d", got)
	}
}

func TestTranscriptBoundsThousandBlocks(t *testing.T) {
	sh := newTestShell(t)
	sh.maxVisible = 300
	for i := 0; i < 1000; i++ {
		sh.addNotice(fmt.Sprintf("notice %d", i))
	}
	if len(sh.visible) != 300 || len(sh.hidden) != 700 {
		t.Fatalf("window: visible=%d hidden=%d", len(sh.visible), len(sh.hidden))
	}
	if got := len(sh.chatBox.Objects); got != 300 {
		t.Fatalf("layout objects: %d", got)
	}
	// Restoring one chunk must not re-render the whole history at once.
	sh.showEarlier()
	if len(sh.visible) != 600 || len(sh.hidden) != 400 {
		t.Fatalf("restore: visible=%d hidden=%d", len(sh.visible), len(sh.hidden))
	}
}

func TestShellEventRendering(t *testing.T) {
	sh := newTestShell(t)

	sh.handleEvent(normalizeEvent(map[string]any{
		"type":   "provider.state",
		"health": map[string]any{"state": "online", "model": "m", "ctx_window": 1000},
	}))
	if got := sh.status.Text; got == "" || got == "model server: connecting…" {
		t.Fatalf("status not updated: %q", got)
	}

	sh.handleEvent(normalizeEvent(map[string]any{"type": "run.started", "run": 1}))
	sh.handleEvent(normalizeEvent(map[string]any{"type": "text.delta", "run": 1, "text": "hel"}))
	sh.handleEvent(normalizeEvent(map[string]any{"type": "text.delta", "run": 1, "text": "lo"}))
	sh.handleEvent(normalizeEvent(map[string]any{"type": "message.final", "run": 1, "text": "**hello**"}))
	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "tool.start", "run": 1, "id": "t1", "name": "Read", "input": `{"file_path":"x"}`,
	}))
	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "tool.result", "run": 1, "id": "t1", "name": "Read", "error": false, "preview": "ok",
	}))
	if len(sh.chatBox.Objects) < 3 {
		t.Fatalf("transcript blocks: %d", len(sh.chatBox.Objects))
	}

	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "todo.updated",
		"items": []any{
			map[string]any{"content": "step one", "status": "done"},
			map[string]any{"content": "step two", "status": "in_progress"},
		},
	}))
	if len(sh.todos.Objects) != 2 {
		t.Fatalf("todos: %d", len(sh.todos.Objects))
	}

	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "perm.request", "id": "p1", "tool": "Bash", "message": "run?", "grant": "Bash(cmd)", "input": "cmd",
	}))
	if len(sh.permRows) != 1 {
		t.Fatalf("permission cards: %d", len(sh.permRows))
	}
	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "perm.resolved", "id": "p1", "reason": "allow",
	}))
	if len(sh.permRows) != 0 {
		t.Fatalf("permission card not removed: %d", len(sh.permRows))
	}

	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "memory.stage",
		"op":   map[string]any{"id": "mem-0123456789abcdef", "target": "user", "action": "add", "content": "prefers tabs", "source": "agent"},
	}))
	sh.handleEvent(normalizeEvent(map[string]any{
		"type":    "skill.stage",
		"op":      map[string]any{"id": "skill-0123456789abcdef", "action": "create", "name": "s", "source": "review"},
		"preview": "new skill s",
	}))
	if len(sh.permRows) != 2 {
		t.Fatalf("stage cards: %d", len(sh.permRows))
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "memory.resolved", "id": "mem-0123456789abcdef", "approved": true}))
	sh.handleEvent(normalizeEvent(map[string]any{"type": "skill.resolved", "id": "skill-0123456789abcdef", "approved": false}))
	if len(sh.permRows) != 0 {
		t.Fatalf("stage cards not removed: %d", len(sh.permRows))
	}

	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "run.finished", "run": 1, "ok": true, "text": "**hello**", "error": "",
		"usage": map[string]any{"tin": 10, "tout": 5}, "ctx_pct": 7,
	}))
	if got := sh.usage.Text; got == "" || got == "running…" {
		t.Fatalf("usage not updated: %q", got)
	}

	// Session replay must replace the transcript with normalized blocks.
	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "session.opened", "id": "s1", "title": "t",
		"blocks": []any{
			map[string]any{"kind": "user", "text": "hi", "name": "", "input": "", "error": false},
			map[string]any{"kind": "assistant", "text": "hello", "name": "", "input": "", "error": false},
		},
	}))
	if len(sh.chatBox.Objects) != 2 {
		t.Fatalf("replay blocks: %d", len(sh.chatBox.Objects))
	}
}
