package agent

import (
	"context"
	"fmt"
	"strings"

	"fyagent/core/tieindex"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/session"
)

// sessionSearchTool exposes the tie word-token index to the agent.
func (r *Runner) sessionSearchTool() dive.Tool {
	type searchInput struct {
		Query string `json:"query" description:"words to AND-search across past sessions (exact tokens, case-insensitive)"`
		Limit int    `json:"limit,omitempty" description:"max hits (default 10, max 50)"`
	}
	return dive.FuncTool("session_search",
		"Search past conversation sessions (user, assistant and tool messages) for ALL given words. Returns the matching snippets with session ids and dates. Use it to recall earlier work instead of guessing.",
		func(ctx context.Context, in *searchInput) (*dive.ToolResult, error) {
			if r.index == nil || !r.index.Enabled() {
				return dive.NewToolResultText("session_search: index disabled"), nil
			}
			hits, err := r.index.Search(in.Query, in.Limit)
			if err != nil {
				return dive.NewToolResultText("session_search: " + err.Error()), nil
			}
			if len(hits) == 0 {
				return dive.NewToolResultText("session_search: no matches."), nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "%d matches (newest first):\n", len(hits))
			for _, h := range hits {
				tool := ""
				if h.Tool != "" {
					tool = ":" + h.Tool
				}
				fmt.Fprintf(&sb, "- [%s] %s %s%s: %s\n", h.SessionID, h.MsgAt, h.Role, tool, clip(h.Snippet, 300))
			}
			return dive.NewToolResultText(sb.String()), nil
		},
		dive.WithFuncToolAnnotations(&dive.ToolAnnotations{ReadOnlyHint: true, Title: "Session search"}))
}

// indexSessionNow pushes the session's messages into the tie index
// (incremental; safe to call after every run).
func (r *Runner) indexSessionNow(sess *session.Session, modelID string) {
	if r.index == nil || !r.index.Enabled() {
		return
	}
	sd := SessionToData(context.Background(), sess, r.cwd, modelID)
	if err := r.index.IndexSession(sd); err != nil {
		r.emit(map[string]any{"type": "tie.state", "state": r.index.State()})
	}
}

// RebuildIndex re-indexes every stored session from disk (full refresh).
// Returns the number of sessions indexed.
func (r *Runner) RebuildIndex(ctx context.Context) (int, error) {
	if r.index == nil || !r.index.Enabled() {
		return 0, fmt.Errorf("tie index disabled")
	}
	res, err := r.store.List(ctx, nil)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, info := range res.Sessions {
		sess, err := r.store.Open(ctx, info.ID)
		if err != nil {
			continue
		}
		if err := r.index.IndexSession(SessionToData(ctx, sess, r.cwd, "")); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// SessionToData flattens a dive session into indexable rows.
func SessionToData(ctx context.Context, sess *session.Session, cwd, modelID string) tieindex.SessionData {
	sd := tieindex.SessionData{
		ID:      sess.ID(),
		Title:   sess.Title(),
		Project: cwd,
		Model:   modelID,
		Updated: nowUTC(),
	}
	msgs, err := sess.Messages(ctx)
	if err != nil {
		return sd
	}
	pos := 0
	for _, m := range msgs {
		for _, c := range m.Content {
			switch v := c.(type) {
			case *llm.TextContent:
				if v.Text == "" || m.Role == llm.System {
					continue
				}
				sd.Msgs = append(sd.Msgs, tieindex.MsgData{Position: pos, Role: string(m.Role), Text: v.Text})
				pos++
			case *llm.ToolUseContent:
				sd.Msgs = append(sd.Msgs, tieindex.MsgData{Position: pos, Role: "assistant", Tool: v.Name, Text: string(v.Input)})
				pos++
			case *llm.ToolResultContent:
				sd.Msgs = append(sd.Msgs, tieindex.MsgData{Position: pos, Role: "tool", Text: resultText(v)})
				pos++
			}
		}
	}
	return sd
}
