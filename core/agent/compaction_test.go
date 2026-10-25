package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/events"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

type recapLLM struct{ got int }

func (f *recapLLM) Name() string { return "recap" }

func (f *recapLLM) Generate(ctx context.Context, opts ...llm.Option) (*llm.Response, error) {
	req := &llm.Config{}
	for _, o := range opts {
		o(req)
	}
	f.got = len(req.Messages)
	return &llm.Response{
		Role: llm.Assistant,
		Content: []llm.Content{
			&llm.TextContent{Text: "<summary>RECAP: earlier work summarized</summary>"},
		},
	}, nil
}

func TestPreGenCompactionHook(t *testing.T) {
	r := &Runner{cfg: &config.Config{}, bus: events.New()}

	// Build a transcript well over a tiny threshold.
	var msgs []*llm.Message
	for i := 0; i < 20; i++ {
		role := llm.User
		if i%2 == 1 {
			role = llm.Assistant
		}
		msgs = append(msgs, &llm.Message{Role: role, Content: []llm.Content{
			&llm.TextContent{Text: strings.Repeat("filler content. ", 100)},
		}})
	}
	hctx := &dive.HookContext{Messages: msgs, Values: map[string]any{}}

	fake := &recapLLM{}
	hook := r.preGenCompactionHook(fake, "SYS", 500)
	if err := hook(context.Background(), hctx); err != nil {
		t.Fatal(err)
	}
	if fake.got == 0 {
		t.Fatal("summary model never called")
	}
	if len(hctx.Messages) >= len(msgs) {
		t.Fatalf("messages not compacted: %d -> %d", len(msgs), len(hctx.Messages))
	}
	var found bool
	for _, m := range hctx.Messages {
		if strings.Contains(m.Text(), "RECAP") {
			found = true
		}
	}
	if !found {
		t.Fatal("summary message missing from compacted transcript")
	}
	if _, ok := hctx.Values[dive.StateKeyCompactionEvent]; !ok {
		t.Fatal("compaction event not recorded in state")
	}

	// Below threshold: no model call, no change.
	fake2 := &recapLLM{}
	hook2 := r.preGenCompactionHook(fake2, "SYS", 1_000_000)
	hctx2 := &dive.HookContext{Messages: msgs}
	if err := hook2(context.Background(), hctx2); err != nil {
		t.Fatal(err)
	}
	if fake2.got != 0 {
		t.Fatal("model called below threshold")
	}
}

func TestCompactionThreshold(t *testing.T) {
	if got := compactionThreshold(0, 82); got != 100000 {
		t.Fatalf("no-window default: %d", got)
	}
	if got := compactionThreshold(262144, 82); got != 214958 {
		t.Fatalf("262144*82%%: %d", got)
	}
	if got := compactionThreshold(1000, 10); got != 8000 {
		t.Fatalf("floor: %d", got)
	}
}
