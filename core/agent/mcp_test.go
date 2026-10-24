package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/deepnoodle-ai/dive"
)

type captureTool struct{ got any }

func (c *captureTool) Name() string         { return "cap" }
func (c *captureTool) Description() string  { return "" }
func (c *captureTool) Schema() *dive.Schema { return nil }
func (c *captureTool) Annotations() *dive.ToolAnnotations { return nil }
func (c *captureTool) Call(ctx context.Context, input any) (*dive.ToolResult, error) {
	c.got = input
	return dive.NewToolResultText("ok"), nil
}

func TestRepairInputShapes(t *testing.T) {
	// dive hands tool input as []byte (openaicompletions Input: []byte(args))
	cap := &captureTool{}
	q := &qualifiedTool{base: cap, name: "mcp__demo__echo"}
	double := json.RawMessage(`"{\"text\":\"hello mcp\"}"`)
	if _, err := q.Call(context.Background(), double); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	switch v := cap.got.(type) {
	case json.RawMessage:
		json.Unmarshal(v, &m)
	case []byte:
		t.Fatalf("base got []byte (adapter default-branch trap): %q", v)
	default:
		t.Fatalf("base got %T %v", v, v)
	}
	if m["text"] != "hello mcp" {
		t.Fatalf("repaired map wrong: %v", m)
	}

	// plain object passes through as RawMessage
	cap2 := &captureTool{}
	q2 := &qualifiedTool{base: cap2, name: "x"}
	q2.Call(context.Background(), json.RawMessage(`{"a":1}`))
	if _, ok := cap2.got.(json.RawMessage); !ok {
		t.Fatalf("plain object reshaped to %T", cap2.got)
	}
}
