package agent

import (
	"context"
	"sort"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/experimental/mcp"
)

// SetMCP attaches the MCP manager (nil = no MCP tools). Tools are re-read
// per run so RefreshTools effects show up without a restart. The manager is
// attached asynchronously at boot, hence the lock.
func (r *Runner) SetMCP(mgr *mcp.Manager) {
	r.mcpMu.Lock()
	r.mcp = mgr
	r.mcpMu.Unlock()
}

func (r *Runner) mcpManager() *mcp.Manager {
	r.mcpMu.Lock()
	defer r.mcpMu.Unlock()
	return r.mcp
}

// mcpTools returns MCP-provided tools in stable (sorted) order under
// server-qualified names (mcp__<server>__<tool>). dive's Manager registers
// unqualified names, which collide with built-ins and mislead models about
// their shape, so we rename via a thin wrapper. (The input-normalization
// wrapper this file once carried is gone: dive passes []byte and the mcp
// adapter handles it since v1.33.0 — deepnoodle-ai/dive#309.)
func mcpTools(mgr *mcp.Manager) []dive.Tool {
	if mgr == nil {
		return nil
	}
	var out []dive.Tool
	for _, server := range mgr.GetServerNames() {
		for _, t := range mgr.GetToolsByServer(server) {
			out = append(out, &qualifiedTool{base: t, name: mcp.QualifiedToolName(server, t.Name())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// qualifiedTool renames a dive.Tool, passing everything else through.
type qualifiedTool struct {
	base dive.Tool
	name string
}

func (q *qualifiedTool) Name() string                       { return q.name }
func (q *qualifiedTool) Description() string                { return q.base.Description() }
func (q *qualifiedTool) Schema() *dive.Schema               { return q.base.Schema() }
func (q *qualifiedTool) Annotations() *dive.ToolAnnotations { return q.base.Annotations() }
func (q *qualifiedTool) Call(ctx context.Context, input any) (*dive.ToolResult, error) {
	return q.base.Call(ctx, input)
}
