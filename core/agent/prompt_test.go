package agent

import (
	"strings"
	"testing"

	"github.com/uidbz/anneal/core/harness"
)

// TestSystemPromptDeterministic guards prompt-cache stability: identical
// inputs must produce a byte-identical prefix. Anything that changes per
// turn (timestamps, counters) belongs in a trailing reminder, never here.
func TestSystemPromptDeterministic(t *testing.T) {
	layers := []harness.Layer{{Name: "system", Content: "be precise"}}
	a := SystemPrompt("/tmp/proj", "default", layers)
	b := SystemPrompt("/tmp/proj", "default", layers)
	if a != b {
		t.Fatal("system prompt is not deterministic")
	}
	for _, want := range []string{
		"Read files with Read",
		"Prefer Edit with a small unique old_string",
		"never prefix commands",
	} {
		if !strings.Contains(a, want) {
			t.Fatalf("system prompt missing %q", want)
		}
	}
}
