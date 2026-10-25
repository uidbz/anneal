package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/experimental/mcp"
)

type captureTool struct {
	got any
}

func (c *captureTool) Name() string                       { return "cap" }
func (c *captureTool) Description() string                { return "" }
func (c *captureTool) Schema() *dive.Schema               { return nil }
func (c *captureTool) Annotations() *dive.ToolAnnotations { return nil }
func (c *captureTool) Call(ctx context.Context, input any) (*dive.ToolResult, error) {
	c.got = input
	return dive.NewToolResultText("ok"), nil
}

// The wrapper must rename but pass input through untouched — the mcp
// adapter handles dive's []byte input natively since v1.33.0.
func TestQualifiedToolPassThrough(t *testing.T) {
	cap := &captureTool{}
	q := &qualifiedTool{base: cap, name: mcp.QualifiedToolName("demo", "echo")}
	if q.Name() != "mcp__demo__echo" {
		t.Fatalf("qualified name: %s", q.Name())
	}
	raw := json.RawMessage(`{"text":"hello mcp"}`)
	if _, err := q.Call(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	g, ok := cap.got.(json.RawMessage)
	if !ok || string(g) != `{"text":"hello mcp"}` {
		t.Fatalf("input rewritten: %T %v", cap.got, cap.got)
	}
}
