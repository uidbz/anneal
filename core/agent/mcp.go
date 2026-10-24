package agent

import (
	"context"
	"encoding/json"

	"sort"
	"strings"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/experimental/mcp"
)

// SetMCP attaches the MCP manager (nil = no MCP tools). Tools are re-read
// per run so RefreshTools effects show up without a restart.
func (r *Runner) SetMCP(mgr *mcp.Manager) { r.mcp = mgr }

// mcpTools returns MCP-provided tools in stable (sorted) order under
// server-qualified names (mcp_<server>_<tool>) — unqualified MCP names
// collide with built-ins and mislead models about their shape.
func mcpTools(mgr *mcp.Manager) []dive.Tool {
	if mgr == nil {
		return nil
	}
	var out []dive.Tool
	for _, server := range mgr.GetServerNames() {
		for _, t := range mgr.GetToolsByServer(server) {
			out = append(out, &qualifiedTool{base: t, name: qualifiedName(server, t.Name())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// qualifiedName mirrors dive's Claude-Code-style "mcp__<server>__<tool>"
// naming (v1.9.0 keeps its helper unexported).
func qualifiedName(server, tool string) string {
	sanit := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
				b.WriteRune(r)
			} else {
				b.WriteByte('_')
			}
		}
		return b.String()
	}
	return "mcp__" + sanit(server) + "__" + sanit(tool)
}

// qualifiedTool renames a dive.Tool while passing everything else through.
type qualifiedTool struct {
	base dive.Tool
	name string
}

func (q *qualifiedTool) Name() string                       { return q.name }
func (q *qualifiedTool) Description() string                { return q.base.Description() }
func (q *qualifiedTool) Schema() *dive.Schema               { return q.base.Schema() }
func (q *qualifiedTool) Annotations() *dive.ToolAnnotations { return q.base.Annotations() }
func (q *qualifiedTool) Call(ctx context.Context, input any) (*dive.ToolResult, error) {
	return q.base.Call(ctx, repairInput(input))
}

// repairInput fixes the JSON-string-wrapped object some local models emit
// as tool input (e.g. `"{\"text\":\"hi\"}"`), which dive's MCP adapter
// cannot unmarshal into an argument map.
func repairInput(input any) any {
	var raw []byte
	switch v := input.(type) {
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return input
	}
	// dive hands input as plain []byte, which mcp's adapter type-switch
	// misses (its default branch base64-mangles it). Normalize to RawMessage.
	if len(raw) == 0 || raw[0] != '"' {
		return json.RawMessage(raw)
	}
	var inner string
	if err := json.Unmarshal(raw, &inner); err != nil {
		return json.RawMessage(raw)
	}
	var probe map[string]any
	if json.Unmarshal([]byte(inner), &probe) != nil {
		return json.RawMessage(raw)
	}
	return json.RawMessage(inner)
}
