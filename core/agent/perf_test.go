package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/uidbz/anneal/core/events"
	"github.com/uidbz/anneal/core/paths"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

func TestPerfTracerSummary(t *testing.T) {
	t.Setenv("ANNEAL_HOME", t.TempDir())
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	var seen []string
	var stats perfSummary
	bus.AddTap(func(v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		typ, _ := m["type"].(string)
		seen = append(seen, typ)
		if s, ok := m["stats"].(perfSummary); ok {
			stats = s
		}
	})

	p := newPerfTracer(bus, 7)
	_, cs := p.StartChat(context.Background(), dive.ChatInfo{
		Iteration:    0,
		Model:        "m",
		SystemPrompt: strings.Repeat("x", 4000),
	})
	cs.SetTimeToFirstChunk(0.5)
	cs.SetResponse(&llm.Response{Usage: llm.Usage{
		InputTokens: 1000, CacheReadInputTokens: 900, OutputTokens: 100,
	}})
	cs.End(nil)
	_, ts := p.StartToolCall(context.Background(), dive.ToolCallInfo{
		Call: &llm.ToolUseContent{Name: "Bash"},
	})
	ts.End(nil)

	sum := p.summarize(1500*time.Millisecond, 200*time.Millisecond)
	if sum.PermWaitMs != 1500 || sum.PostMs != 200 {
		t.Fatalf("wait/post = %d/%d", sum.PermWaitMs, sum.PostMs)
	}
	if sum.In != 1000 || sum.Cached != 900 || sum.Out != 100 {
		t.Fatalf("tokens = %+v", sum)
	}
	if sum.TTFTMs != 500 || sum.TTFTMaxMs != 500 {
		t.Fatalf("ttft = %d/%d", sum.TTFTMs, sum.TTFTMaxMs)
	}
	if len(sum.Iters) != 1 || len(sum.Tools) != 1 || sum.Iters[0].InputEst != 1000 {
		t.Fatalf("iter/tools = %+v %+v", sum.Iters, sum.Tools)
	}

	p.finish(0, 0)
	want := map[string]bool{"model.wait": true, "model.first_token": true, "run.stats": true}
	for _, typ := range seen {
		delete(want, typ)
	}
	if len(want) != 0 {
		t.Fatalf("missing events %v (got %v)", want, seen)
	}
	if stats.Run != 7 {
		t.Fatalf("run.stats payload = %+v", stats)
	}
	if _, err := os.Stat(paths.PerfLog()); err != nil {
		t.Fatalf("perf log not written: %v", err)
	}
}

func TestFormatPerf(t *testing.T) {
	out := FormatPerf(map[string]any{"stats": map[string]any{
		"run": float64(3), "total_ms": float64(12000), "model_ms": float64(8000),
		"tool_ms": float64(2000), "perm_wait_ms": float64(1500), "post_ms": float64(500),
		"in": float64(50000), "cached": float64(40000), "out": float64(1200),
		"reasoning": float64(600), "tps": float64(31.5),
		"ttft_ms": float64(2500), "ttft_max_ms": float64(5000),
		"iters": []any{map[string]any{"iter": float64(0), "ttft_ms": float64(2500),
			"duration_ms": float64(8000), "in": float64(50000), "cached": float64(40000),
			"out": float64(1200)}},
	}})
	for _, want := range []string{"run #3", "model 8.0s", "approvals 1.5s", "prompt 90000", "44%", "32 tok/s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("FormatPerf missing %q:\n%s", want, out)
		}
	}
}
