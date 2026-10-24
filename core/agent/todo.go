package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/deepnoodle-ai/dive"
)

// Todo checklist: a session-scoped plan-of-record the model maintains with
// the todo tool; every change is pushed to the UI (todo.updated event).

type todoItem struct {
	Content string `json:"content" description:"one imperative task line"`
	Status  string `json:"status" description:"task state" enum:"pending,in_progress,done,cancelled"`
}

type todoInput struct {
	Action string     `json:"action" description:"set replaces the whole list; list reads it" enum:"set,list"`
	Items  []todoItem `json:"items,omitempty" description:"full replacement list for action=set (at most one in_progress)"`
}

func (r *Runner) todoTool() dive.Tool {
	return dive.FuncTool("todo",
		"Maintain a living checklist for multi-step work. Call action=set with the FULL updated list whenever anything changes; keep at most one item in_progress. Use list to re-read it.",
		func(ctx context.Context, in *todoInput) (*dive.ToolResult, error) {
			r.mu.Lock()
			switch in.Action {
			case "set":
				r.todos = append([]todoItem(nil), in.Items...)
			case "list":
			default:
				r.mu.Unlock()
				return dive.NewToolResultError(`todo: action must be "set" or "list"`), nil
			}
			items := append([]todoItem{}, r.todos...)
			r.mu.Unlock()
			if in.Action == "set" {
				r.emit(map[string]any{"type": "todo.updated", "items": items})
			}
			return dive.NewToolResultText(renderTodos(items)), nil
		},
		// Planning is a read activity: keep todo usable in plan mode.
		dive.WithFuncToolAnnotations(&dive.ToolAnnotations{ReadOnlyHint: true, Title: "Todo checklist"}))
}

func renderTodos(items []todoItem) string {
	if len(items) == 0 {
		return "No todos."
	}
	var sb strings.Builder
	done := 0
	for i, it := range items {
		glyph := "[ ]"
		switch it.Status {
		case "in_progress":
			glyph = "[>]"
		case "done":
			glyph = "[x]"
			done++
		case "cancelled":
			glyph = "[-]"
		}
		fmt.Fprintf(&sb, "%d. %s %s\n", i+1, glyph, it.Content)
	}
	fmt.Fprintf(&sb, "(%d/%d done)", done, len(items))
	return sb.String()
}
