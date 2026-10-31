package agent

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/dive"
)

func TestSequentialToolHint(t *testing.T) {
	base := dive.FuncTool("mutate", "writes", func(context.Context, *struct{}) (*dive.ToolResult, error) {
		return dive.NewToolResultText("ok"), nil
	})
	wrapped := seq(base)
	ann := wrapped.Annotations()
	if ann == nil || !ann.SequentialOnlyHint {
		t.Fatal("mutating tool is not marked sequential")
	}
	if wrapped.Name() != "mutate" || wrapped.Description() != "writes" || wrapped.Schema() == nil {
		t.Fatal("wrapper changed the tool surface")
	}
	// Marking one call serializes its whole batch; pure read batches still
	// run in parallel (dive checks every call in the batch).
}
