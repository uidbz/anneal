package agent

import (
	"context"
	"fmt"
	"strings"
)

// Digest renders a bounded transcript of one stored session for the
// background reviewer (approximates maxBytes; per-message clip 600 chars).
func (r *Runner) Digest(ctx context.Context, sessionID string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 24000
	}
	sess, err := r.store.Open(ctx, sessionID)
	if err != nil {
		return ""
	}
	sd := SessionToData(ctx, sess, r.cwd, "")
	var sb strings.Builder
	title := sess.Title()
	if title == "" {
		title = sessionID
	}
	fmt.Fprintf(&sb, "## session %s — %s\n", sessionID, title)
	for _, m := range sd.Msgs {
		tag := strings.ToUpper(m.Role)
		if m.Tool != "" {
			tag = "TOOL-CALL " + m.Tool
		}
		fmt.Fprintf(&sb, "[%s] %s\n", tag, clip(m.Text, 600))
		if sb.Len() > maxBytes {
			return sb.String() + "\n…[digest truncated]"
		}
	}
	return sb.String()
}
