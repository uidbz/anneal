package review

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/memory"
	"github.com/uidbz/anneal/core/skills"

	"github.com/deepnoodle-ai/dive/llm"
)

func TestFlagStoreLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q", "harness-queue.json")
	s := NewFlagStore(path)
	first := s.Add("sess1", "done declared without re-running tests")
	s.Add("sess2", "repeats the same failing tool call unchanged")
	got := s.Pending(5)
	if len(got) != 2 || got[0].ID != first.ID {
		t.Fatalf("pending: %+v", got)
	}
	taken := s.Consume("run1", 1)
	if len(taken) != 1 || taken[0].ID != first.ID {
		t.Fatalf("consume: %+v", taken)
	}
	if p := s.Pending(5); len(p) != 1 || p[0].ConsumedBy != "" {
		t.Fatalf("after consume: %+v", p)
	}
	// persistence: a fresh store sees the same state
	s2 := NewFlagStore(path)
	if p := s2.Pending(5); len(p) != 1 || !strings.Contains(p[0].Gap, "failing tool call") {
		t.Fatalf("reload: %+v", p)
	}
	if c := s2.Consume("run2", 9); len(c) != 1 || c[0].ConsumedBy != "run2" {
		t.Fatalf("consume2: %+v", c)
	}
	if len(s2.Pending(5)) != 0 {
		t.Fatal("queue should be drained")
	}
}

func TestFlagStoreRelease(t *testing.T) {
	s := NewFlagStore(filepath.Join(t.TempDir(), "q.json"))
	s.Add("s", "repeats the same failing tool call unchanged")
	if got := s.Consume("run1", 1); len(got) != 1 {
		t.Fatalf("consume: %+v", got)
	}
	s.Release("run1")
	p := s.Pending(5)
	if len(p) != 1 || p[0].ConsumedBy != "" {
		t.Fatalf("release did not restore flag: %+v", p)
	}
	// Releasing another run must not touch it.
	s.Consume("run2", 1)
	s.Release("run1")
	if p := s.Pending(5); len(p) != 0 {
		t.Fatalf("unrelated release changed state: %+v", p)
	}
}

func TestReviewerHarnessFlag(t *testing.T) {
	t.Setenv("ANNEAL_HOME", t.TempDir())
	mem := memory.NewStore(&config.Memory{Enabled: true, MemoryCharLimit: 500, UserCharLimit: 500})
	sk := skills.NewStore(&config.Skills{Enabled: true})

	var mu sync.Mutex
	var types []string
	emit := func(v any) {
		mu.Lock()
		defer mu.Unlock()
		switch e := v.(type) {
		case Event:
			types = append(types, e.Type)
		case map[string]any:
			types = append(types, toStr(e["type"]))
		}
	}
	fake := &scriptedLLM{resps: []*llm.Response{
		toolCallResp("harness_flag", map[string]any{"gap": "declares done without verifying tests"}),
		toolCallResp("harness_flag", map[string]any{"gap": "hmm"}),
		textResp("flagged one gap"),
	}}
	m := NewManager(
		&config.Review{Enabled: true, IdleSettleS: 0, MaxAgeS: 60, MaxInputTokens: 4000},
		mem, sk, nil, emit,
		func() llm.LLM { return fake },
	)
	fs := NewFlagStore(filepath.Join(t.TempDir(), "flags.json"))
	m.SetFlagStore(fs)
	m.Digest = func(ctx context.Context, sid string) string { return "## " + sid + "\n[ASSIST] all done!" }

	m.Enqueue("s9")
	m.runOnce(context.Background())

	pending := fs.Pending(5)
	if len(pending) != 1 { // "too short" (<8 chars) rejected by the tool
		t.Fatalf("flags: %+v", pending)
	}
	if pending[0].Gap != "declares done without verifying tests" || pending[0].Session != "s9" {
		t.Fatalf("flag: %+v", pending[0])
	}
	mu.Lock()
	defer mu.Unlock()
	var flagEvt bool
	for _, ty := range types {
		if ty == "review.flag" {
			flagEvt = true
		}
	}
	if !flagEvt {
		t.Fatalf("no review.flag event in %v", types)
	}
}

func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
