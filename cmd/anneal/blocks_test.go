package main

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
)

func TestToolResultUpdatesRowByID(t *testing.T) {
	sh := newTestShell(t)
	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "tool.start", "run": 1, "id": "t1", "name": "Read",
		"input": `{"file_path":"core/app.go"}`,
	}))
	row := sh.toolRows["t1"]
	if row == nil {
		t.Fatal("tool row not tracked")
	}
	before := len(sh.chatBox.Objects)
	sh.handleEvent(normalizeEvent(map[string]any{
		"type": "tool.result", "run": 1, "id": "t1", "name": "Read",
		"error": false, "preview": "package app",
	}))
	if len(sh.chatBox.Objects) != before {
		t.Fatalf("result appended a block: %d -> %d", before, len(sh.chatBox.Objects))
	}
	if row.marker.Text() != "✔" {
		t.Fatalf("marker = %q", row.marker.Text())
	}
	if row.preview == nil || row.preview.Text != "package app" {
		t.Fatalf("preview = %+v", row.preview)
	}
	if !row.detail.Hidden {
		t.Fatal("result preview should start collapsed")
	}
	row.setExpanded(true)
	if row.detail.Hidden {
		t.Fatal("preview did not expand")
	}
}

func TestToolReplayPairsResults(t *testing.T) {
	sh := newTestShell(t)
	sh.renderBlock(map[string]any{"kind": "tool", "text": "", "name": "Read", "input": `{"file_path":"a.go"}`, "error": false})
	sh.renderBlock(map[string]any{"kind": "toolres", "text": "contents of a", "name": "", "input": "", "error": false})
	// One caption+row object and one assistant caption only when the first
	// tool block opens the turn.
	if len(sh.pendingTools) != 0 {
		t.Fatalf("replay left %d pending tool rows", len(sh.pendingTools))
	}
	if len(sh.chatBox.Objects) != 1 {
		t.Fatalf("blocks = %d", len(sh.chatBox.Objects))
	}
}

func TestImproveProgressUsesOneLine(t *testing.T) {
	sh := newTestShell(t)
	base := len(sh.chatBox.Objects)
	sh.handleEvent(normalizeEvent(map[string]any{"type": "improve.start", "run": "r1"}))
	sh.handleEvent(normalizeEvent(map[string]any{"type": "improve.propose", "kind": "skills"}))
	sh.handleEvent(normalizeEvent(map[string]any{"type": "improve.probe", "kind": "skills", "steps": 12, "duration_s": 34}))
	if got := len(sh.chatBox.Objects) - base; got != 1 {
		t.Fatalf("progress appended %d blocks, want 1", got)
	}
	line := sh.progressLines["improve"]
	if line == nil || !strings.Contains(line.text.Text(), "probing skills") {
		t.Fatalf("progress text = %+v", line)
	}
	sh.handleEvent(normalizeEvent(map[string]any{"type": "improve.done", "ok": true, "successor": true}))
	if sh.progressLines["improve"] != nil {
		t.Fatal("finished progress line still tracked")
	}
	if got := len(sh.chatBox.Objects) - base; got != 1 {
		t.Fatalf("finish appended a block: %d", got)
	}
}

func TestWelcomeState(t *testing.T) {
	sh := newTestShell(t)
	if sh.welcome == nil || !sh.welcome.Visible() {
		t.Fatal("welcome not visible in an empty session")
	}
	sh.addUser("hello")
	if sh.welcome.Visible() {
		t.Fatal("welcome still visible after the first block")
	}
	sh.resetChat()
	if !sh.welcome.Visible() {
		t.Fatal("welcome not restored by resetChat")
	}
}

func TestSpeakerCaptionsOncePerTurn(t *testing.T) {
	sh := newTestShell(t)
	sh.addUser("hi")
	sh.addMarkdown("first answer")
	sh.addMarkdown("second block of the same turn")
	sh.addTool("t1", "Read", `{"file_path":"x"}`)
	sh.addMarkdown("after the tool")
	if sh.turn != turnAssistant {
		t.Fatalf("turn = %v", sh.turn)
	}
	// Two user/assistant turns' worth of captions: check the assistant
	// caption appears exactly once by walking the first block's VBox.
	first := sh.visible[1].(*fyne.Container)
	if len(first.Objects) != 2 {
		t.Fatalf("first assistant block objects = %d", len(first.Objects))
	}
}

func TestToolSummaryKeepsPrimaryArgument(t *testing.T) {
	cases := map[string]string{
		`{"file_path":"core/app.go"}`:         "core/app.go",
		`{"command":"go test ./..."}`:         "go test ./...",
		`{"pattern":"func Split","path":"x"}`: "func Split",
		`not json`:                            "not json",
	}
	for in, want := range cases {
		if got := toolSummary(in); got != want {
			t.Errorf("toolSummary(%q) = %q, want %q", in, got, want)
		}
	}
	if got := toolDisplayName("mcp__gortex__search"); got != "gortex.search" {
		t.Fatalf("toolDisplayName = %q", got)
	}
}
