package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/deepnoodle-ai/dive"
)

// repeatGuard catches the issue #13 pattern: the model retries the same
// failing call (sometimes with a slightly different input) instead of
// changing approach. The second identical failure adds an instruction to the
// tool result; dive shows it to the model as extra context.
type repeatGuard struct {
	mu     sync.Mutex
	counts map[string]int
}

func newRepeatGuard() *repeatGuard {
	return &repeatGuard{counts: map[string]int{}}
}

func (g *repeatGuard) hook() dive.PostToolUseFailureHook {
	return func(_ context.Context, hctx *dive.HookContext) error {
		if hctx.Call == nil {
			return nil
		}
		key := hctx.Call.Name + "\x00" + errorClass(hctx.Result)
		g.mu.Lock()
		g.counts[key]++
		n := g.counts[key]
		g.mu.Unlock()
		if n >= 2 {
			hctx.AdditionalContext = fmt.Sprintf(
				"This %s call failed the same way %d times. Do not retry the same call shape: "+
					"change the arguments, pass the required context, or switch tools "+
					"(Read/Grep/Glob).", hctx.Call.Name, n)
		}
		return nil
	}
}

// errorClass is a stable fingerprint of a tool failure (first line, clipped).
func errorClass(res *dive.ToolCallResult) string {
	text := ""
	if res != nil {
		switch {
		case res.Error != nil:
			text = res.Error.Error()
		case res.Result != nil:
			text = toolResultText(res.Result)
		}
	}
	text = strings.ToLower(strings.TrimSpace(text))
	if i := strings.IndexByte(text, '\n'); i > 0 {
		text = text[:i]
	}
	if len(text) > 160 {
		text = text[:160]
	}
	return text
}
