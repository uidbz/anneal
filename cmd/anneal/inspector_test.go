package main

import (
	"strings"
	"testing"
)

func TestInspectorShowsWorkingDirectory(t *testing.T) {
	sh := newTestShell(t)
	sh.inspector.refresh()
	want := shortPath(sh.a.Runner.Cwd(), 34)
	if got := sh.inspector.sessCwd.Text; got != want {
		t.Fatalf("cwd label = %q, want %q", got, want)
	}
	if !strings.Contains(want, "…/") && len(sh.a.Runner.Cwd()) > 34 {
		t.Fatalf("long path not shortened: %q", want)
	}
}

func TestShortPath(t *testing.T) {
	if got := shortPath("/a/b", 34); got != "/a/b" {
		t.Fatalf("short path changed: %q", got)
	}
	long := "/home/someone/very/deep/project/directory/that/is/long"
	got := shortPath(long, 20)
	if !strings.HasPrefix(got, "…/") || len([]rune(got)) > 21 {
		t.Fatalf("shortPath = %q", got)
	}
	if !strings.HasSuffix(long, strings.TrimPrefix(got, "…/")) {
		t.Fatalf("shortPath lost the tail: %q", got)
	}
}

func TestCwdCommand(t *testing.T) {
	sh := newTestShell(t)
	before := len(sh.chatBox.Objects)
	sh.slash("/cwd")
	if len(sh.chatBox.Objects) != before+1 {
		t.Fatalf("no notice appended: %d -> %d", before, len(sh.chatBox.Objects))
	}
	text := paneTexts(sh.visible[len(sh.visible)-1])
	if !strings.Contains(text, "Working directory:") || !strings.Contains(text, sh.a.Runner.Cwd()) {
		t.Fatalf("cwd notice = %q", text)
	}
}
