package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/uidbz/anneal/core/events"
)

type todoCallResult struct {
	text  string
	isErr bool
}

func callTodo(t *testing.T, r *Runner, in map[string]any) todoCallResult {
	t.Helper()
	res, err := r.todoTool().Call(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if c.Text != "" {
			sb.WriteString(c.Text)
		}
	}
	return todoCallResult{text: sb.String(), isErr: res.IsError}
}

func TestTodoLenientSetAndValidation(t *testing.T) {
	r := &Runner{bus: events.New()}
	// items without an action are treated as set (the reported failure).
	res := callTodo(t, r, map[string]any{"items": []any{
		map[string]any{"content": "phase A", "status": "done"},
		map[string]any{"content": "phase B", "status": "in_progress"},
	}})
	if res.isErr || !strings.Contains(res.text, "phase B") {
		t.Fatalf("lenient set = %+v", res)
	}
	if len(r.todos) != 2 {
		t.Fatalf("todos = %+v", r.todos)
	}

	bad := callTodo(t, r, map[string]any{"items": []any{
		map[string]any{"content": "x", "status": "doing"},
	}})
	if !bad.isErr || !strings.Contains(bad.text, "pending|in_progress|done|cancelled") {
		t.Fatalf("bad status = %+v", bad)
	}

	two := callTodo(t, r, map[string]any{"action": "set", "items": []any{
		map[string]any{"content": "a", "status": "in_progress"},
		map[string]any{"content": "b", "status": "in_progress"},
	}})
	if !two.isErr || !strings.Contains(two.text, "at most one") {
		t.Fatalf("two in_progress = %+v", two)
	}

	missing := callTodo(t, r, map[string]any{})
	if !missing.isErr || !strings.Contains(missing.text, "action is required") {
		t.Fatalf("missing action = %+v", missing)
	}
}

func TestTodoStaleReminderOncePerEpisode(t *testing.T) {
	r := &Runner{bus: events.New()}
	r.todos = []todoItem{{Content: "phase A", Status: "pending"}}
	r.todoCallsAt = 0
	r.toolCalls = 20
	r.todoUpdatedAt = time.Now().Add(-10 * time.Minute)

	if _, ok := r.takeTodoStaleReminder(); !ok {
		t.Fatal("stale checklist not reported")
	}
	if _, ok := r.takeTodoStaleReminder(); ok {
		t.Fatal("stale reminder repeated in the same episode")
	}
	// A fully done list is not stale.
	r.todoWarned = false
	r.todos[0].Status = "done"
	if _, ok := r.takeTodoStaleReminder(); ok {
		t.Fatal("a fully done list was reported stale")
	}
}
