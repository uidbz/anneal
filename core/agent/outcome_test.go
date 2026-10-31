package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/dive"
)

func TestSummarizeTurnOutcome(t *testing.T) {
	calls := []dive.ToolCallRecord{
		{Name: "Write", State: dive.ToolCallStateCompleted},
		{Name: "Bash", State: dive.ToolCallStateNotStarted},
		{Name: "Edit", State: dive.ToolCallStateUnknown},
	}
	got := summarizeTurnOutcome("deadline", calls)
	for _, want := range []string{"Time limit reached", "1 step(s) completed", "not run: Bash", "unknown: Edit (check before continuing)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q: %s", want, got)
		}
	}
}

func TestTurnOutcomeEventBudget(t *testing.T) {
	b := &turnBudget{}
	b.setExceeded()
	ev := turnOutcomeEvent(nil, b)
	if ev == nil || ev["reason"] != "budget" || ev["next"] != "continue" {
		t.Fatalf("budget outcome = %+v", ev)
	}
	if !strings.Contains(ev["summary"].(string), "Turn budget exhausted") {
		t.Fatalf("summary = %v", ev["summary"])
	}
	if turnOutcomeEvent(nil, &turnBudget{}) != nil {
		t.Fatal("a completed turn produced an outcome")
	}
}

func TestBudgetHookFinalCall(t *testing.T) {
	b := &turnBudget{}
	b.setFinal()
	hctx := dive.NewHookContext()
	if err := b.hook()(context.Background(), hctx); err != nil {
		t.Fatal(err)
	}
	if !hctx.FinalCall {
		t.Fatal("final budget state did not disable tools for the last call")
	}
	// The warning is delivered once, then cleared.
	b2 := &turnBudget{}
	b2.setWarn()
	h1 := dive.NewHookContext()
	if err := b2.hook()(context.Background(), h1); err != nil {
		t.Fatal(err)
	}
	h2 := dive.NewHookContext()
	if err := b2.hook()(context.Background(), h2); err != nil {
		t.Fatal(err)
	}
	if h2.FinalCall {
		t.Fatal("warning promoted itself to a final call")
	}
}
