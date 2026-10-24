package agent

import (
	"context"
	"fmt"
	"strings"

	"fyagent/core/memory"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

// memoryTool lets the agent persist durable facts (MEMORY.md) and user
// preferences (USER.md). With write_approval on, writes are staged for the
// user instead of applied.
func (r *Runner) memoryTool() dive.Tool {
	type memInput struct {
		Target  string `json:"target" description:"'memory' for project/world facts and conventions, 'user' for the user's preferences"`
		Action  string `json:"action" description:"add | patch | remove"`
		Content string `json:"content,omitempty" description:"entry text for add"`
		Old     string `json:"old,omitempty" description:"unique substring identifying the text to patch/remove"`
		New     string `json:"new,omitempty" description:"replacement text for patch"`
		Reason  string `json:"reason,omitempty" description:"one-line justification (shown to the user when staging)"`
	}
	return dive.FuncTool("memory",
		"Save durable knowledge: add/patch/remove entries in two small bounded stores — target 'memory' (facts, conventions, environment quirks) and 'user' (preferences, habits). Entries re-enter every future session, so keep them short, factual and current (patch/remove stale ones). Do NOT store conversation play-by-play; use session_search for past conversations.",
		func(ctx context.Context, in *memInput) (*dive.ToolResult, error) {
			if r.mem == nil || !r.cfg.Memory.Enabled {
				return dive.NewToolResultText("memory: disabled"), nil
			}
			op := &memory.Op{
				Target: in.Target, Action: in.Action,
				Content: in.Content, Old: in.Old, New: in.New,
				Reason: in.Reason, Source: "agent",
			}
			if err := r.mem.Validate(op); err != nil {
				return dive.NewToolResultText("memory: " + err.Error()), nil
			}
			if r.cfg.Memory.WriteApproval {
				staged, err := r.mem.Stage(op)
				if err != nil {
					return dive.NewToolResultText("memory: " + err.Error()), nil
				}
				r.emit(map[string]any{"type": "memory.stage", "op": staged})
				return dive.NewToolResultText(fmt.Sprintf(
					"memory: staged for user approval (id %s). It is NOT saved yet — the user sees a diff and approves or rejects.", staged.ID)), nil
			}
			content, err := r.mem.Apply(op)
			if err != nil {
				return dive.NewToolResultText("memory: " + err.Error()), nil
			}
			r.emit(map[string]any{"type": "memory.updated", "target": op.Target,
				"action": op.Action, "content": content})
			return dive.NewToolResultText(fmt.Sprintf(
				"memory: %s ok — %s now %d/%d chars.", op.Action, op.Target,
				len(content), r.mem.Limit(op.Target))), nil
		},
		// Read-only hint keeps planning (plan mode) possible; the actual
		// write gate is write_approval staging, not the permission manager.
		dive.WithFuncToolAnnotations(&dive.ToolAnnotations{ReadOnlyHint: true, Title: "Memory"}))
}

// memoryReminderHook injects the memory snapshot as a named <system-reminder>
// block in the first user message exactly once per session (frozen snapshot);
// dive replaces same-named blocks in place, so nothing accumulates.
func (r *Runner) memoryReminderHook() dive.PreGenerationHook {
	return func(ctx context.Context, hctx *dive.HookContext) error {
		if r.mem == nil || !r.cfg.Memory.Enabled {
			return nil
		}
		if hasReminder(hctx.Messages, "fyagent-memory") {
			return nil
		}
		snap := r.mem.Snapshot()
		if snap == "" {
			return nil
		}
		hctx.Messages = dive.SetSystemReminder(hctx.Messages, "fyagent-memory", snap)
		return nil
	}
}

func hasReminder(msgs []*llm.Message, name string) bool {
	tag := `<system-reminder name="` + name + `"`
	for _, m := range msgs {
		for _, c := range m.Content {
			if tc, ok := c.(*llm.TextContent); ok && strings.Contains(tc.Text, tag) {
				return true
			}
		}
	}
	return false
}
