package agent

import (
	"context"
	"strings"

	"github.com/uidbz/anneal/core/memory"
	"github.com/uidbz/anneal/core/toolset"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

// memoryTool lets the agent persist durable facts (MEMORY.md) and user
// preferences (USER.md). With write_approval on, writes are staged for the
// user instead of applied.
func (r *Runner) memoryTool() dive.Tool {
	return toolset.MemoryTool(r.mem, "agent", toolset.MemoryCallbacks{
		Stage: func(_ *memory.Op, staged *memory.Op) {
			r.emit(map[string]any{"type": "memory.stage", "op": staged})
		},
		Apply: func(op *memory.Op, content string) {
			r.emit(map[string]any{"type": "memory.updated", "target": op.Target,
				"action": op.Action, "content": content})
		},
	})
}

// memoryReminderHook injects the memory snapshot as a named <system-reminder>
// block in the first user message exactly once per session (frozen snapshot);
// dive replaces same-named blocks in place, so nothing accumulates.
func (r *Runner) memoryReminderHook() dive.PreGenerationHook {
	return func(ctx context.Context, hctx *dive.HookContext) error {
		if r.mem == nil || !r.cfg.Memory.Enabled {
			return nil
		}
		if hasReminder(hctx.Messages, "anneal-memory") {
			return nil
		}
		snap := r.mem.Snapshot()
		if snap == "" {
			return nil
		}
		hctx.Messages = dive.SetSystemReminder(hctx.Messages, "anneal-memory", snap)
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
