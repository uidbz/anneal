package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/uidbz/anneal/core/events"
	"github.com/uidbz/anneal/core/paths"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

func TestStashCall(t *testing.T) {
	t.Setenv("ANNEAL_HOME", t.TempDir())
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	r := &Runner{bus: events.New()}
	hctx := &dive.HookContext{Call: &llm.ToolUseContent{
		ID:    "call_1",
		Name:  "Write",
		Input: json.RawMessage(`{"file_path":"x.txt","content":"hello"}`),
	}}
	path := r.stashCall(hctx)
	if path == "" {
		t.Fatal("write input not stashed")
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "hello") {
		t.Fatalf("stash = %s (%v)", data, err)
	}
	if again := r.stashCall(hctx); again != path {
		t.Fatalf("stash path changed: %q vs %q", again, path)
	}
	// Non-mutating tools have nothing to re-apply.
	if got := r.stashCall(&dive.HookContext{Call: &llm.ToolUseContent{
		ID: "call_2", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`),
	}}); got != "" {
		t.Fatalf("bash stashed at %q", got)
	}
}
