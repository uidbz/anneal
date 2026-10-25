package main

import (
	"strings"
	"testing"
)

func TestNormalizeEvent(t *testing.T) {
	type nested struct {
		State string `json:"state"`
	}
	ev := normalizeEvent(map[string]any{
		"type":   "provider.state",
		"run":    3,
		"health": nested{State: "online"},
	})
	if ev["type"] != "provider.state" {
		t.Fatalf("type: %v", ev["type"])
	}
	if num(ev["run"]) != 3 {
		t.Fatalf("run: %v", ev["run"])
	}
	h := obj(ev["health"])
	if str(h["state"]) != "online" {
		t.Fatalf("nested struct not normalized: %#v", h)
	}
}

func TestEventHelpers(t *testing.T) {
	m := map[string]any{"s": "x", "n": 2, "b": true, "o": map[string]any{"k": "v"}, "l": []any{1, 2}}
	if str(m["s"]) != "x" || num(m["n"]) != 2 || !boolean(m["b"]) {
		t.Fatal("scalar helpers")
	}
	if str(obj(m["o"])["k"]) != "v" || len(list(m["l"])) != 2 {
		t.Fatal("map/list helpers")
	}
	if got := oneLine("a\nb"); got != "a b" {
		t.Fatalf("oneLine: %q", got)
	}
	if got := oneLine(strings.Repeat("x", 300)); len([]rune(got)) != 241 {
		t.Fatalf("oneLine clip: %d", len(got))
	}
}

func TestPaneEventFilter(t *testing.T) {
	for _, typ := range []string{"improve.done", "harness.promoted", "review.flag", "config.changed"} {
		if !paneEvent(map[string]any{"type": typ}) {
			t.Errorf("%s filtered out", typ)
		}
	}
	for _, typ := range []string{"text.delta", "tool.start", "perm.request", "todo.updated", "memory.stage"} {
		if paneEvent(map[string]any{"type": typ}) {
			t.Errorf("%s reached the pane", typ)
		}
	}
}

func TestEmbeddedPaneSource(t *testing.T) {
	t.Setenv("ANNEAL_PANE_DIR", "")
	t.Setenv("ANNEAL_UI_DIR", "")
	t.Setenv("FYAGENT_PANE_DIR", "")
	t.Setenv("FYAGENT_UI_DIR", "")
	src, err := paneSource("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(src, "HARNESS-PANE-READY") || !strings.Contains(src, "agent.harnessInfo") ||
		!strings.Contains(src, "agent.on") {
		t.Fatalf("unexpected embedded pane: %.120q", src)
	}
}
