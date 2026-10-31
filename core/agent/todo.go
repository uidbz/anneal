package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/deepnoodle-ai/dive"
)

// Todo checklist: a session-scoped plan-of-record the model maintains with
// the todo tool; every change is pushed to the UI (todo.updated event).

type todoItem struct {
	Content string `json:"content" description:"one imperative task line"`
	Status  string `json:"status" description:"task state" enum:"pending,in_progress,done,cancelled"`
}

type todoInput struct {
	Action string     `json:"action" description:"required: set replaces the whole list, list reads it (set is assumed when items are given)" enum:"set,list"`
	Items  []todoItem `json:"items,omitempty" description:"full replacement list for action=set (at most one in_progress)"`
}

func (r *Runner) todoTool() dive.Tool {
	return dive.FuncTool("todo",
		"Maintain a living checklist for multi-step work. action is required: "+
			"action=set with the FULL updated list whenever anything changes "+
			"(at most one item in_progress); action=list re-reads it. Update the list "+
			"as phases complete — a stale list misleads the user.",
		func(ctx context.Context, in *todoInput) (*dive.ToolResult, error) {
			switch in.Action {
			case "":
				if len(in.Items) > 0 {
					in.Action = "set" // be forgiving: items imply set
				} else {
					return dive.NewToolResultError(
						`todo: action is required — {"action":"set","items":[{"content":"…","status":"pending"}]} or {"action":"list"}`), nil
				}
			case "set", "list":
			default:
				return dive.NewToolResultError(
					`todo: action must be "set" or "list" (got ` + in.Action + `)`), nil
			}
			if in.Action == "set" {
				inProgress := 0
				for i := range in.Items {
					switch in.Items[i].Status {
					case "pending", "in_progress", "done", "cancelled":
					default:
						return dive.NewToolResultError(fmt.Sprintf(
							"todo: item %d has status %q; use pending|in_progress|done|cancelled",
							i+1, in.Items[i].Status)), nil
					}
					if in.Items[i].Status == "in_progress" {
						inProgress++
					}
				}
				if inProgress > 1 {
					return dive.NewToolResultError("todo: at most one item may be in_progress"), nil
				}
			}

			r.mu.Lock()
			switch in.Action {
			case "set":
				r.todos = append([]todoItem(nil), in.Items...)
				r.todoCallsAt = r.toolCalls
				r.todoUpdatedAt = time.Now()
				r.todoWarned = false
			}
			items := append([]todoItem{}, r.todos...)
			sess := r.sess
			r.mu.Unlock()

			if in.Action == "set" {
				// Persist immediately: a crash or a new session must not lose
				// the plan-of-record, and the sidebar/inspector may read it.
				if sess != nil {
					sess.SetMetadata("todos", items)
					if r.store != nil {
						_ = r.store.Put(context.Background(), sess)
					}
				}
				r.emit(map[string]any{"type": "todo.updated", "items": items,
					"stale": false, "calls_since": 0})
			}
			return dive.NewToolResultText(renderTodos(items)), nil
		},
		// Planning is a read activity: keep todo usable in plan mode.
		dive.WithFuncToolAnnotations(&dive.ToolAnnotations{ReadOnlyHint: true, Title: "Todo checklist"}))
}

// todoSnapshotEvent is the replay form of todo.updated (includes staleness).
func (r *Runner) todoSnapshotEvent() map[string]any {
	r.mu.Lock()
	items := append([]todoItem{}, r.todos...)
	since := r.toolCalls - r.todoCallsAt
	stale := r.todoWarned
	r.mu.Unlock()
	return map[string]any{"type": "todo.updated", "items": items,
		"stale": stale, "calls_since": since}
}

// countToolCall feeds stagnation detection: a plan that has not been touched
// after enough tool calls is probably stale.
func (r *Runner) countToolCall() {
	r.mu.Lock()
	r.toolCalls++
	r.mu.Unlock()
}

// todoReminderHook nudges the model when an unfinished checklist has not been
// updated for a while. It is a ModelOnly reminder appended at the end of the
// conversation, so it never disturbs the cached prompt prefix.
func (r *Runner) todoReminderHook() dive.PreIterationHook {
	return func(_ context.Context, hctx *dive.HookContext) error {
		text, ok := r.takeTodoStaleReminder()
		if !ok {
			return nil
		}
		if rem, err := dive.NewContextReminder("todo-stale", text); err == nil {
			_ = hctx.AppendReminder(rem, dive.ModelOnly)
		}
		return nil
	}
}

// takeTodoStaleReminder reports staleness once per episode (until the next
// todo set) and emits the UI signal.
func (r *Runner) takeTodoStaleReminder() (string, bool) {
	const staleCalls = 15
	r.mu.Lock()
	if r.todoWarned || len(r.todos) == 0 {
		r.mu.Unlock()
		return "", false
	}
	unfinished := false
	for _, it := range r.todos {
		if it.Status == "in_progress" || it.Status == "pending" {
			unfinished = true
			break
		}
	}
	if !unfinished || r.toolCalls-r.todoCallsAt < staleCalls {
		r.mu.Unlock()
		return "", false
	}
	r.todoWarned = true
	items := append([]todoItem{}, r.todos...)
	since := r.toolCalls - r.todoCallsAt
	age := time.Since(r.todoUpdatedAt)
	r.mu.Unlock()

	r.emit(map[string]any{"type": "todo.stale", "stale": true,
		"calls_since": since, "age_s": int(age.Seconds())})
	return "The todo checklist has not been updated for " + fmt.Sprintf("%d tool calls", since) +
		" and still has unfinished items. Update it now (todo action=set with the full list: " +
		"mark completed phases done, set the current one in_progress) so the user can follow the work.\n" +
		renderTodos(items), true
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
