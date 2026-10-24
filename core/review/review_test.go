package review

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"fyagent/core/config"
	"fyagent/core/memory"
	"fyagent/core/skills"

	"github.com/deepnoodle-ai/dive/llm"
)

type scriptedLLM struct {
	mu    sync.Mutex
	resps []*llm.Response
	calls int
}

func (f *scriptedLLM) Name() string { return "scripted" }

func (f *scriptedLLM) Generate(ctx context.Context, opts ...llm.Option) (*llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls >= len(f.resps) {
		return &llm.Response{Role: llm.Assistant, StopReason: "end_turn",
			Content: []llm.Content{&llm.TextContent{Text: "done"}},
		}, nil
	}
	r := f.resps[f.calls]
	f.calls++
	return r, nil
}

func toolCallResp(name string, input map[string]any) *llm.Response {
	raw, _ := json.Marshal(input)
	return &llm.Response{Role: llm.Assistant, StopReason: "tool_use",
		Content: []llm.Content{&llm.ToolUseContent{ID: "tu-1", Name: name, Input: raw}}}
}

func textResp(text string) *llm.Response {
	return &llm.Response{Role: llm.Assistant, StopReason: "end_turn",
		Content: []llm.Content{&llm.TextContent{Text: text}}}
}

func TestReviewerLedgerAndApply(t *testing.T) {
	t.Setenv("FYAGENT_HOME", t.TempDir())
	mem := memory.NewStore(config.Memory{Enabled: true, MemoryCharLimit: 500, UserCharLimit: 500})
	sk := skills.NewStore(config.Skills{Enabled: true})

	var mu sync.Mutex
	var events []Event
	emit := func(v any) {
		if e, ok := v.(Event); ok {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		}
	}
	fake := &scriptedLLM{resps: []*llm.Response{
		toolCallResp("memory", map[string]any{"target": "user", "action": "add",
			"content": "prefers tabs", "reason": "stated twice"}),
		textResp("saved one preference"),
	}}

	m := NewManager(
		config.Review{Enabled: true, IdleSettleS: 0, MaxAgeS: 60, MaxInputTokens: 4000},
		config.Memory{Enabled: true, MemoryCharLimit: 500, UserCharLimit: 500},
		config.Skills{Enabled: true},
		mem, sk, nil, emit,
		func() llm.LLM { return fake },
	)
	m.Digest = func(ctx context.Context, sid string) string { return "## session " + sid + "\n[USER] use tabs always" }

	m.Enqueue("sess-1")
	m.runOnce(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0].Type != "review.start" || events[1].Type != "review.done" {
		t.Fatalf("events: %+v", events)
	}
	done := events[1]
	if len(done.Ledger) != 1 {
		t.Fatalf("ledger empty: %+v", done)
	}
	if done.Ledger[0].Kind != "memory" || done.Ledger[0].Action != "add" || !strings.HasPrefix(done.Ledger[0].Result, "applied") {
		t.Fatalf("ledger entry: %+v", done.Ledger[0])
	}
	cur, _ := mem.Read("user")
	if cur != "- prefers tabs\n" {
		t.Fatalf("memory not applied: %q", cur)
	}
}

func TestReviewerStagingLedger(t *testing.T) {
	t.Setenv("FYAGENT_HOME", t.TempDir())
	mem := memory.NewStore(config.Memory{Enabled: true, MemoryCharLimit: 500, UserCharLimit: 500, WriteApproval: true})
	sk := skills.NewStore(config.Skills{Enabled: true})
	var staged []any
	emit := func(v any) {
		if m, ok := v.(map[string]any); ok && m["type"] == "memory.stage" {
			staged = append(staged, m["op"])
		}
	}
	fake := &scriptedLLM{resps: []*llm.Response{
		toolCallResp("memory", map[string]any{"target": "memory", "action": "add", "content": "fact from review"}),
		textResp("staged one"),
	}}
	m := NewManager(config.Review{Enabled: true}, config.Memory{Enabled: true, MemoryCharLimit: 500, UserCharLimit: 500, WriteApproval: true},
		config.Skills{Enabled: true}, mem, sk, nil, emit, func() llm.LLM { return fake })
	m.Digest = func(context.Context, string) string { return "digest" }
	m.Enqueue("s")
	m.runOnce(context.Background())

	if len(staged) != 1 {
		t.Fatalf("no memory.stage event emitted: %v", staged)
	}
	if cur, _ := mem.Read("memory"); cur != "" {
		t.Fatal("staged write applied directly")
	}
	list, _ := mem.ListPending()
	if len(list) != 1 || list[0].Source != "review" {
		t.Fatalf("pending: %+v", list)
	}
}

func TestShouldRunIdleGating(t *testing.T) {
	m := NewManager(config.Review{Enabled: true, DeferUntilIdle: true, IdleSettleS: 3600, MaxAgeS: 1},
		config.Memory{}, config.Skills{}, nil, nil, nil, func(any) {}, func() llm.LLM { return nil })
	m.Enqueue("x")
	if m.shouldRun() {
		t.Fatal("ran despite huge idle settle")
	}
	// max-age fallback: firstQue older than MaxAgeS -> allowed
	m.mu.Lock()
	m.firstQue = m.firstQue.Add(-10 * 60e9)
	m.mu.Unlock()
	if !m.shouldRun() {
		t.Fatal("max-age fallback ignored")
	}
	m.SetActivityHooks(func() bool { return true }, m.lastAct)
	if m.shouldRun() {
		t.Fatal("ran while busy")
	}
}
