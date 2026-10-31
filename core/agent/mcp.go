package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

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
func mcpTools(mgr *mcp.Manager, skipGortex bool) []dive.Tool {
	if mgr == nil {
		return nil
	}
	var out []dive.Tool
	for _, server := range mgr.GetServerNames() {
		if skipGortex && server == "gortex" {
			continue
		}
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
	res, err := q.base.Call(ctx, input)
	return mcpErrorHint(q.name, res, err)
}

// mcpErrorHints turn recurring, non-actionable MCP errors into instructions.
// gortex's "repository not tracked" used to be retried unchanged three times
// in a row (issue #13); the hint names the fix and forbids the retry.
var mcpErrorHints = []struct {
	match string
	hint  string
}{
	{
		match: "repository not tracked",
		hint: "Hint: gortex only searches repositories registered with `gortex track` " +
			"(list them with `gortex repos`). The working directory is not tracked. " +
			"Do not retry this call unchanged: pass an explicit path= (or repo=) for a " +
			"tracked repository, or switch to Grep/Glob for this search.",
	},
}

func mcpErrorHint(name string, res *dive.ToolResult, err error) (*dive.ToolResult, error) {
	text := ""
	if err != nil {
		text = err.Error()
	} else if res != nil {
		for _, c := range res.Content {
			text += c.Text
		}
	}
	if text == "" {
		return res, err
	}
	for _, h := range mcpErrorHints {
		if !strings.Contains(text, h.match) {
			continue
		}
		if err != nil {
			return res, fmt.Errorf("%w\n\n%s", err, h.hint)
		}
		if res == nil {
			return res, nil
		}
		res.Content = append(res.Content, &dive.ToolResultContent{
			Type: dive.ToolResultContentTypeText, Text: "\n\n" + h.hint,
		})
		return res, nil
	}
	return res, err
}

// SetGortexStatus records whether the working directory is tracked by gortex
// and which repositories are. Tools are omitted when it is not.
func (r *Runner) SetGortexStatus(untracked bool, tracked []string) {
	r.gortexMu.Lock()
	r.gortexChecked = true
	r.gortexUntracked = untracked
	r.gortexTracked = append([]string(nil), tracked...)
	r.gortexMu.Unlock()
}

func (r *Runner) isGortexUntracked() bool {
	r.gortexMu.Lock()
	defer r.gortexMu.Unlock()
	return r.gortexChecked && r.gortexUntracked
}

func (r *Runner) gortexEvent() map[string]any {
	r.gortexMu.Lock()
	defer r.gortexMu.Unlock()
	if !r.gortexChecked || !r.gortexUntracked {
		return nil
	}
	return map[string]any{"type": "gortex.untracked", "cwd": r.cwd,
		"tracked": append([]string(nil), r.gortexTracked...)}
}
