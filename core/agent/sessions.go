package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"fyagent/core/paths"

	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/session"
)

// Sessions are dive JSONL files under ~/.fyagent/sessions (FileStore).
// The store is the conversation truth; the runner holds the open one.

func newSessionID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s", time.Now().Format("20060102-150405"), hex.EncodeToString(b))
}

// initStore opens the FileStore and resumes the most recent session (or
// creates the first one).
func (r *Runner) initStore() error {
	store, err := session.NewFileStore(paths.Sessions())
	if err != nil {
		return err
	}
	r.store = store

	ctx := context.Background()
	id := ""
	if res, err := store.List(ctx, &session.ListOptions{Limit: 1}); err == nil && len(res.Sessions) > 0 {
		id = res.Sessions[0].ID
	}
	if id == "" {
		id = newSessionID()
	}
	sess, err := store.Open(ctx, id)
	if err != nil {
		return err
	}
	r.sess, r.sessID = sess, id
	r.restoreTodos(sess)
	return nil
}

// restoreTodos reloads the persisted todo list (session metadata) so a
// resumed session keeps its checklist.
func (r *Runner) restoreTodos(sess *session.Session) {
	r.todos = nil
	md := sess.Metadata()
	if md == nil {
		return
	}
	raw, ok := md["todos"]
	if !ok {
		return
	}
	if b, err := json.Marshal(raw); err == nil {
		var items []todoItem
		if err := json.Unmarshal(b, &items); err == nil {
			r.todos = items
		}
	}
}

// SessionsJSON lists sessions (newest first) for the sidebar.
func (r *Runner) SessionsJSON() string {
	res, err := r.store.List(context.Background(), nil)
	if err != nil {
		return "[]"
	}
	out := make([]map[string]any, 0, len(res.Sessions))
	for _, s := range res.Sessions {
		out = append(out, map[string]any{
			"id":      s.ID,
			"title":   s.Title,
			"updated": s.UpdatedAt.Format("2006-01-02 15:04"),
			"events":  s.EventCount,
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// NewSession starts a fresh session (no run may be active).
func (r *Runner) NewSession() {
	if r.busy() {
		r.emit(map[string]any{"type": "run.busy"})
		return
	}
	id := newSessionID()
	sess, err := r.store.Open(context.Background(), id)
	if err != nil {
		r.emit(map[string]any{"type": "session.error", "id": id, "error": err.Error()})
		return
	}
	r.mu.Lock()
	r.sess, r.sessID = sess, id
	r.todos = nil
	r.mu.Unlock()
	r.emit(map[string]any{"type": "session.started", "id": id, "title": ""})
}

// OpenSession resumes a stored session and replays its transcript to the UI.
func (r *Runner) OpenSession(id string) {
	if r.busy() {
		r.emit(map[string]any{"type": "run.busy"})
		return
	}
	sess, err := r.store.Open(context.Background(), id)
	if err != nil {
		r.emit(map[string]any{"type": "session.error", "id": id, "error": err.Error()})
		return
	}
	blocks := transcriptBlocks(context.Background(), sess)
	r.mu.Lock()
	r.sess, r.sessID = sess, id
	r.restoreTodos(sess)
	todos := append([]todoItem{}, r.todos...)
	r.mu.Unlock()
	r.emit(map[string]any{"type": "session.opened", "id": id, "title": sess.Title(), "blocks": blocks})
	if len(todos) > 0 {
		r.emit(map[string]any{"type": "todo.updated", "items": todos})
	}
}

func (r *Runner) busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// transcriptBlocks converts stored messages into normalized UI blocks. Every
// block carries every key so Risor dot-access never hits a missing key.
func transcriptBlocks(ctx context.Context, sess *session.Session) []map[string]any {
	msgs, err := sess.Messages(ctx)
	if err != nil {
		return []map[string]any{}
	}
	blocks := []map[string]any{}
	for _, m := range msgs {
		for _, c := range m.Content {
			switch v := c.(type) {
			case *llm.TextContent:
				if v.Text == "" || m.Role == llm.System {
					continue
				}
				kind := "user"
				if m.Role == llm.Assistant {
					kind = "assistant"
				}
				blocks = append(blocks, block(kind, v.Text, "", "", false))
			case *llm.ToolUseContent:
				blocks = append(blocks, block("tool", "", v.Name, string(v.Input), false))
			case *llm.ToolResultContent:
				blocks = append(blocks, block("toolres", resultText(v), "", "", v.IsError))
			}
		}
	}
	return blocks
}

func block(kind, text, name, input string, err bool) map[string]any {
	return map[string]any{"kind": kind, "text": clip(text, 2000), "name": name, "input": clip(input, 300), "error": err}
}

func resultText(v *llm.ToolResultContent) string {
	switch c := v.Content.(type) {
	case string:
		return c
	case nil:
		return ""
	default:
		if b, err := json.Marshal(c); err == nil {
			return string(b)
		}
		return fmt.Sprint(c)
	}
}

