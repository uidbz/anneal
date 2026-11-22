package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/uidbz/anneal/core/paths"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/session"
)

// Sessions are dive JSONL files under ~/.anneal/sessions (FileStore).
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
	// Resume the newest session that belongs to this project. One state dir
	// can hold several projects' sessions; resuming another project's
	// conversation would silently mix jails. Legacy sessions without cwd are
	// a fallback so an upgrade does not look like data loss.
	id := ""
	fallback := ""
	if res, err := store.List(ctx, nil); err == nil {
		for _, s := range res.Sessions {
			cwd := ""
			if s.Metadata != nil {
				cwd, _ = s.Metadata["cwd"].(string)
			}
			// Scheduled-job sessions are owned by the scheduler; resuming one
			// in the foreground would hide the user's actual conversation.
			if origin, _ := s.Metadata["origin"].(string); origin == "schedule" {
				continue
			}
			if cwd == "" && fallback == "" {
				fallback = s.ID
			}
			if cwd == r.cwd {
				id = s.ID
				break
			}
		}
	}
	if id == "" {
		id = fallback
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
	// Record the project so the next boot can resume the right session.
	if md := sess.Metadata(); md == nil || md["cwd"] == nil {
		sess.SetMetadata("cwd", r.cwd)
		_ = store.Put(ctx, sess)
	}
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
			r.todoUpdatedAt = time.Now()
			r.todoCallsAt = r.toolCalls
			r.todoWarned = false
		}
	}
}

// SessionInfo is one row for a session picker.
type SessionInfo struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Updated string `json:"updated"`
	Events  int    `json:"events"`
	// Cwd is the project directory the session was created in ("" for
	// sessions recorded before this was tracked).
	Cwd string `json:"cwd,omitempty"`
	// Origin is "user" for normal sessions and "schedule" for job runs.
	Origin string `json:"origin,omitempty"`
	JobID  string `json:"job_id,omitempty"`
}

// Sessions lists sessions (newest first) for UI code. FileStore.List parses
// every session file, and the shell asks for the list several times per event
// (and per filter keystroke), so results are cached briefly and invalidated on
// session changes.
func (r *Runner) Sessions() []SessionInfo {
	r.sessMu.Lock()
	if r.sessCache != nil && time.Since(r.sessCacheAt) < 2*time.Second {
		out := append([]SessionInfo(nil), r.sessCache...)
		r.sessMu.Unlock()
		return out
	}
	r.sessMu.Unlock()

	res, err := r.store.List(context.Background(), nil)
	if err != nil {
		return []SessionInfo{}
	}
	out := make([]SessionInfo, 0, len(res.Sessions))
	for _, s := range res.Sessions {
		cwd := ""
		origin := ""
		jobID := ""
		if s.Metadata != nil {
			cwd, _ = s.Metadata["cwd"].(string)
			origin, _ = s.Metadata["origin"].(string)
			jobID, _ = s.Metadata["job_id"].(string)
		}
		out = append(out, SessionInfo{
			ID:      s.ID,
			Title:   s.Title,
			Updated: s.UpdatedAt.Format("2006-01-02 15:04"),
			Events:  s.EventCount,
			Cwd:     cwd,
			Origin:  origin,
			JobID:   jobID,
		})
	}
	r.sessMu.Lock()
	r.sessCache = out
	r.sessCacheAt = time.Now()
	r.sessMu.Unlock()
	return append([]SessionInfo(nil), out...)
}

// invalidateSessions drops the cached list after a title/new/open change.
func (r *Runner) invalidateSessions() {
	r.sessMu.Lock()
	r.sessCache = nil
	r.sessMu.Unlock()
}

// SessionsJSON lists sessions as a JSON array (script UI bridge).
func (r *Runner) SessionsJSON() string {
	b, err := json.Marshal(r.Sessions())
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
	sess.SetMetadata("cwd", r.cwd)
	sess.SetMetadata("origin", "user")
	_ = r.store.Put(context.Background(), sess)
	// Queued messages belong to the session that was active when typed.
	r.ClearQueue()
	r.mu.Lock()
	r.sess, r.sessID = sess, id
	r.todos = nil
	r.queuePaused, r.queueReason = false, ""
	r.mu.Unlock()
	if r.perm != nil {
		r.perm.ClearSession() // session grants do not follow the user
	}
	r.invalidateSessions()
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
	if md := sess.Metadata(); md == nil || md["origin"] == nil {
		sess.SetMetadata("origin", "user")
		_ = r.store.Put(context.Background(), sess)
	}
	blocks := transcriptBlocks(context.Background(), sess)
	r.ClearQueue()
	r.mu.Lock()
	r.sess, r.sessID = sess, id
	r.restoreTodos(sess)
	todos := append([]todoItem{}, r.todos...)
	r.queuePaused, r.queueReason = false, ""
	r.mu.Unlock()
	if r.perm != nil {
		r.perm.ClearSession()
	}
	r.invalidateSessions()
	r.emit(map[string]any{"type": "session.opened", "id": id, "title": sess.Title(), "blocks": blocks})
	// Always emit: an empty list must clear the previous session's panel.
	r.emit(map[string]any{"type": "todo.updated", "items": todos})
}

// ConfigureSession tags the open session with its project cwd and origin
// (user or schedule) and persists the metadata. Scheduled-job sessions set
// origin=schedule and job_id so the foreground never auto-resumes them.
func (r *Runner) ConfigureSession(cwd, origin, jobID string) {
	r.mu.Lock()
	sess := r.sess
	r.mu.Unlock()
	if sess == nil {
		return
	}
	if cwd == "" {
		cwd = r.cwd
	}
	sess.SetMetadata("cwd", cwd)
	if origin != "" {
		sess.SetMetadata("origin", origin)
	}
	if jobID != "" {
		sess.SetMetadata("job_id", jobID)
	}
	if r.store != nil {
		_ = r.store.Put(context.Background(), sess)
	}
	r.invalidateSessions()
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
		// A turn that stopped short carries its outcome as a recorded
		// reminder; replay it as a transcript notice so a resumed session
		// shows where and why work was interrupted.
		if out, ok := dive.FindTurnOutcome(m); ok && out != nil {
			blocks = append(blocks, block("outcome",
				summarizeTurnOutcome(string(out.Reason), out.ToolCalls), "", "", false))
		}
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
