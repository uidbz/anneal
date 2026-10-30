package main

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	coreapp "github.com/uidbz/anneal/core/app"
)

// TestUICapture renders the shell with a representative session through the
// headless Fyne test driver and writes PNGs for visual review. It only runs
// when ANNEAL_UI_CAPTURE names an output directory:
//
//	ANNEAL_UI_CAPTURE=/tmp/opencode/ui go test ./cmd/anneal -run TestUICapture
func TestUICapture(t *testing.T) {
	dir := os.Getenv("ANNEAL_UI_CAPTURE")
	if dir == "" {
		t.Skip("set ANNEAL_UI_CAPTURE=<dir> to render UI captures")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANNEAL_HOME", t.TempDir())
	a, err := coreapp.New()
	if err != nil {
		t.Fatal(err)
	}
	fyneApp := test.NewApp()
	t.Cleanup(fyneApp.Quit)
	mode := os.Getenv("ANNEAL_UI_CAPTURE_THEME")
	if mode == "" {
		mode = "dark"
	}
	// newShell applies ui.theme, so set it before constructing the shell.
	a.Cfg.UI.Theme = mode
	win := fyneApp.NewWindow("capture")
	src, err := paneSource("")
	if err != nil {
		t.Fatal(err)
	}
	sh, err := newShell(a, fyneApp, win, src)
	if err != nil {
		t.Fatal(err)
	}
	win.SetContent(sh.root)
	win.Resize(fyne.NewSize(1280, 800))
	feedSampleSession(sh)
	// The harness pane is a scripted view; run it so the capture shows it.
	sh.harness.Execute()
	waitPaneReady(t, sh)
	capture(t, win, filepath.Join(dir, "chat-"+mode+".png"))
	for _, v := range []viewID{viewHarness, viewActivity, viewSettings} {
		sh.showView(v)
		capture(t, win, filepath.Join(dir, strings.ToLower(v.String())+"-"+mode+".png"))
	}
}

func capture(t *testing.T, win fyne.Window, path string) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	// The software test canvas does not re-layout on MinSize changes the way
	// the GLFW driver does every frame, so lay the whole tree out top-down.
	relayout(win.Content())
	img := win.Canvas().Capture()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}

func relayout(o fyne.CanvasObject) {
	switch v := o.(type) {
	case *fyne.Container:
		if v.Layout != nil {
			v.Layout.Layout(v.Objects, v.Size())
		}
		for _, c := range v.Objects {
			relayout(c)
		}
	case fyne.Widget:
		r := test.WidgetRenderer(v)
		r.Layout(v.Size())
		for _, c := range r.Objects() {
			relayout(c)
		}
	}
}

// feedSampleSession drives the shell through the same events a real session
// produces: health chips, a replayed transcript, a live tool call, todos, and
// a pending permission.
func feedSampleSession(sh *shell) {
	ev := func(m map[string]any) { sh.handleEvent(normalizeEvent(m)) }
	ev(map[string]any{"type": "provider.state", "health": map[string]any{
		"state": "online", "model": "qwen3-coder-30b-a3b", "ctx_window": 262144}})
	ev(map[string]any{"type": "tie.state", "state": map[string]any{
		"enabled": true, "online": true, "collection": "anneal"}})
	ev(map[string]any{"type": "mcp.state", "servers": []any{
		map[string]any{"name": "gortex", "connected": true, "tools": 14}}})
	ev(map[string]any{"type": "session.opened", "id": "20261030-101500-ab12", "title": "Fix the failing parser test",
		"blocks": []any{
			map[string]any{"kind": "user", "text": "The parser test in core/textutil fails on CRLF input. Find the cause and fix it.", "name": "", "input": "", "error": false},
			map[string]any{"kind": "tool", "text": "", "name": "Grep", "input": `{"pattern":"func Split","path":"core/textutil"}`, "error": false},
			map[string]any{"kind": "toolres", "text": "core/textutil/textutil.go:42: func SplitLines(s string) []string", "name": "", "input": "", "error": false},
			map[string]any{"kind": "tool", "text": "", "name": "Read", "input": `{"file_path":"core/textutil/textutil.go"}`, "error": false},
			map[string]any{"kind": "toolres", "text": "package textutil ...", "name": "", "input": "", "error": false},
			map[string]any{"kind": "assistant", "text": "The splitter only handles `\\n`, so a trailing `\\r` survives on every line.\n\n```go\nfunc SplitLines(s string) []string {\n\ts = strings.ReplaceAll(s, \"\\r\\n\", \"\\n\")\n\treturn strings.Split(s, \"\\n\")\n}\n```\n\n| case | before | after |\n|---|---|---|\n| LF | ok | ok |\n| CRLF | fails | ok |", "name": "", "input": "", "error": false},
		}})
	ev(map[string]any{"type": "mode.changed", "mode": "default"})
	ev(map[string]any{"type": "todo.updated", "items": []any{
		map[string]any{"content": "Reproduce the CRLF failure", "status": "done"},
		map[string]any{"content": "Normalize line endings in SplitLines", "status": "done"},
		map[string]any{"content": "Run the textutil tests", "status": "in_progress"},
		map[string]any{"content": "Add a CRLF regression test", "status": "pending"},
	}})
	sh.addUser("Great — run the tests and add a regression test.")
	ev(map[string]any{"type": "run.started", "run": 2, "model": "qwen3-coder-30b-a3b"})
	ev(map[string]any{"type": "tool.start", "run": 2, "id": "t9", "name": "Bash", "input": `{"command":"go test ./core/textutil/..."}`})
	ev(map[string]any{"type": "tool.result", "run": 2, "id": "t9", "name": "Bash", "error": false, "preview": "ok  github.com/uidbz/anneal/core/textutil 0.012s"})
	ev(map[string]any{"type": "text.delta", "run": 2, "text": "Tests pass. Adding the regression test next."})
	ev(map[string]any{"type": "perm.request", "id": "p1", "tool": "Write", "action": "edit",
		"resources": []any{"core/textutil/textutil_test.go"}, "message": "Write wants to create a file",
		"grant": "edit(core/textutil/*)", "input": `{"file_path":"core/textutil/textutil_test.go"}`})
}

func waitPaneReady(t *testing.T, sh *shell) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := sh.harness.view.Window().Status()
		if st == "Ready!" {
			return
		}
		if strings.HasPrefix(st, "ERROR") {
			t.Fatalf("pane script failed: %s", st)
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane did not finish; status %q", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
