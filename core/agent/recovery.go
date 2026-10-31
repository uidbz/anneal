package agent

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/uidbz/anneal/core/paths"

	"github.com/deepnoodle-ai/dive"
)

// recoveryTTL bounds the stash: a cancelled write is useful for the next few
// turns, not forever.
const recoveryTTL = 7 * 24 * time.Hour

// stashCall saves the full input of a mutating tool call that was cancelled
// (stop, deadline, permission cancellation) so the next turn can re-apply it
// instead of regenerating the content. Returns the file path ("" = nothing
// saved). Only the reported case is covered: a fully composed call waiting for
// approval. Partial generation before the call exists has nothing to apply.
func (r *Runner) stashCall(hctx *dive.HookContext) string {
	if hctx == nil || hctx.Call == nil || len(hctx.Call.Input) == 0 {
		return ""
	}
	switch hctx.Call.Name {
	case "Write", "Edit", "TextEditor", "str_replace_based_edit_tool":
		// mutating file tools only
	default:
		return ""
	}
	sid := r.SessionID()
	if sid == "" {
		sid = "session"
	}
	dir := filepath.Join(paths.Recovery(), safeName(sid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	id := hctx.Call.ID
	if id == "" {
		id = time.Now().UTC().Format("20060102-150405")
	}
	path := filepath.Join(dir, safeName(id)+".json")
	if err := os.WriteFile(path, hctx.Call.Input, 0o600); err != nil {
		return ""
	}
	pruneRecovery(dir, recoveryTTL)
	r.emit(map[string]any{"type": "tool.recovered", "tool": hctx.Call.Name, "path": path})
	return path
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
}

func pruneRecovery(dir string, ttl time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-ttl)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}
