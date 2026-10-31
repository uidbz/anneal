// Per-run timing: where the wall clock actually goes. dive's Tracer fires
// once per model call, once per tool call and once per run, which is exactly
// the split needed to tell prompt processing, generation, tools and waiting
// for the user apart. The summary rides the event bus as run.stats and is
// appended to ~/.anneal/logs/perf.jsonl for before/after comparisons.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/uidbz/anneal/core/events"
	"github.com/uidbz/anneal/core/paths"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

// perfIter is one model call.
type perfIter struct {
	Iter       int    `json:"iter"`
	Model      string `json:"model,omitempty"`
	InputEst   int    `json:"input_est"`
	TTFTms     int64  `json:"ttft_ms"`
	DurationMs int64  `json:"duration_ms"`
	In         int    `json:"in"`
	Cached     int    `json:"cached"`
	Out        int    `json:"out"`
	Reasoning  int    `json:"reasoning"`
}

// perfTool is one tool call.
type perfTool struct {
	Name  string `json:"name"`
	Ms    int64  `json:"ms"`
	Error bool   `json:"error,omitempty"`
}

// perfSummary is the per-run report carried on run.stats.
type perfSummary struct {
	Run        int        `json:"run"`
	TotalMs    int64      `json:"total_ms"`
	ModelMs    int64      `json:"model_ms"`
	ToolMs     int64      `json:"tool_ms"`
	PermWaitMs int64      `json:"perm_wait_ms"`
	PostMs     int64      `json:"post_ms"`
	Iters      []perfIter `json:"iters"`
	Tools      []perfTool `json:"tools"`
	In         int        `json:"in"`
	Cached     int        `json:"cached"`
	Out        int        `json:"out"`
	Reasoning  int        `json:"reasoning"`
	TTFTMs     int64      `json:"ttft_ms"`     // median
	TTFTMaxMs  int64      `json:"ttft_max_ms"` // worst call
	TPS        float64    `json:"tps"`         // output tokens / generation second
}

// perfTracer implements dive.Tracer for one run.
type perfTracer struct {
	bus     *events.Bus
	run     int
	started time.Time

	mu    sync.Mutex
	iters []perfIter
	tools []perfTool
}

func newPerfTracer(bus *events.Bus, run int) *perfTracer {
	return &perfTracer{bus: bus, run: run, started: time.Now()}
}

func (p *perfTracer) emit(v map[string]any) {
	if p.bus != nil {
		p.bus.Emit(v)
	}
}

func (p *perfTracer) StartAgentRun(ctx context.Context, _ dive.AgentRunInfo) (context.Context, dive.AgentRunSpan) {
	return ctx, &perfRunSpan{tracer: p}
}

func (p *perfTracer) StartChat(ctx context.Context, info dive.ChatInfo) (context.Context, dive.ChatSpan) {
	iter := perfIter{Iter: info.Iteration, Model: info.Model, InputEst: estimateTokens(info.SystemPrompt, info.Messages)}
	p.emit(map[string]any{"type": "model.wait", "run": p.run,
		"iteration": info.Iteration, "input_est": iter.InputEst})
	return ctx, &perfChatSpan{tracer: p, iter: iter, started: time.Now()}
}

func (p *perfTracer) StartToolCall(ctx context.Context, info dive.ToolCallInfo) (context.Context, dive.ToolCallSpan) {
	name := ""
	if info.Call != nil {
		name = info.Call.Name
	}
	return ctx, &perfToolSpan{tracer: p, tool: perfTool{Name: name}, started: time.Now()}
}

// estimateTokens is dive's own heuristic (~4 bytes/token), good enough to
// show "processing ~48k of context" while the first token is pending.
func estimateTokens(system string, messages []*llm.Message) int {
	n := len(system)
	for _, m := range messages {
		n += len(m.Text())
	}
	return n / 4
}

type perfRunSpan struct{ tracer *perfTracer }

func (s *perfRunSpan) SetResponse(*dive.Response) {}
func (s *perfRunSpan) SetUsage(*llm.Usage)        {}
func (s *perfRunSpan) End(error)                  {}

type perfChatSpan struct {
	tracer  *perfTracer
	iter    perfIter
	started time.Time
	usage   *llm.Usage
}

func (s *perfChatSpan) SetResponse(resp *llm.Response) {
	if resp != nil {
		u := resp.Usage // a struct, always present
		s.usage = &u
	}
}

func (s *perfChatSpan) SetTimeToFirstChunk(seconds float64) {
	s.iter.TTFTms = int64(seconds * 1000)
	s.tracer.emit(map[string]any{"type": "model.first_token", "run": s.tracer.run,
		"iteration": s.iter.Iter, "ttft_ms": s.iter.TTFTms, "input_est": s.iter.InputEst})
}

func (s *perfChatSpan) End(err error) {
	s.iter.DurationMs = time.Since(s.started).Milliseconds()
	if u := s.usage; u != nil {
		s.iter.In = u.InputTokens
		s.iter.Cached = u.CacheReadInputTokens
		s.iter.Out = u.OutputTokens
		s.iter.Reasoning = u.ReasoningTokens
	}
	_ = err // a failed call is still timed; the summary shows the gap
	s.tracer.mu.Lock()
	s.tracer.iters = append(s.tracer.iters, s.iter)
	s.tracer.mu.Unlock()
}

type perfToolSpan struct {
	tracer  *perfTracer
	tool    perfTool
	started time.Time
	isError bool
}

func (s *perfToolSpan) SetResult(res *dive.ToolCallResult) {
	if res != nil && (res.Error != nil || (res.Result != nil && res.Result.IsError)) {
		s.isError = true
	}
}

func (s *perfToolSpan) End(err error) {
	s.tool.Ms = time.Since(s.started).Milliseconds()
	s.tool.Error = s.isError || err != nil
	s.tracer.mu.Lock()
	s.tracer.tools = append(s.tracer.tools, s.tool)
	s.tracer.mu.Unlock()
}

// summarize builds the run report and appends it to the perf log.
func (p *perfTracer) summarize(permWait, post time.Duration) perfSummary {
	p.mu.Lock()
	iters := append([]perfIter{}, p.iters...)
	tools := append([]perfTool{}, p.tools...)
	p.mu.Unlock()

	sum := perfSummary{Run: p.run, TotalMs: time.Since(p.started).Milliseconds(),
		PermWaitMs: permWait.Milliseconds(), PostMs: post.Milliseconds(),
		Iters: iters, Tools: tools}

	var ttfts []int64
	var genMs int64
	for _, it := range iters {
		sum.ModelMs += it.DurationMs
		sum.In += it.In
		sum.Cached += it.Cached
		sum.Out += it.Out
		sum.Reasoning += it.Reasoning
		if it.TTFTms > 0 {
			ttfts = append(ttfts, it.TTFTms)
			if it.TTFTms > sum.TTFTMaxMs {
				sum.TTFTMaxMs = it.TTFTms
			}
		}
		if it.DurationMs > it.TTFTms {
			genMs += it.DurationMs - it.TTFTms
		}
	}
	for _, t := range tools {
		sum.ToolMs += t.Ms
	}
	if len(ttfts) > 0 {
		sort.Slice(ttfts, func(i, j int) bool { return ttfts[i] < ttfts[j] })
		sum.TTFTMs = ttfts[len(ttfts)/2]
	}
	if genMs > 0 {
		sum.TPS = float64(sum.Out) / (float64(genMs) / 1000)
	}
	return sum
}

// finish emits run.stats and appends the record to the perf log. Failures to
// log are ignored: instrumentation must never fail a run.
func (p *perfTracer) finish(permWait, post time.Duration) perfSummary {
	sum := p.summarize(permWait, post)
	p.emit(map[string]any{"type": "run.stats", "run": sum.Run, "stats": sum})
	if data, err := json.Marshal(map[string]any{
		"at": time.Now().UTC().Format(time.RFC3339), "stats": sum,
	}); err == nil {
		f, err := os.OpenFile(paths.PerfLog(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.Write(append(data, '\n'))
			_ = f.Close()
		}
	}
	return sum
}

// FormatPerf renders a one-screen report for /perf.
func FormatPerf(m map[string]any) string {
	stats := m
	if inner, ok := m["stats"].(map[string]any); ok {
		stats = inner
	}
	if stats == nil {
		return "no run stats yet"
	}
	get := func(k string) float64 {
		if v, ok := stats[k].(float64); ok {
			return v
		}
		return 0
	}
	sec := func(ms float64) string { return fmt.Sprintf("%.1fs", ms/1000) }
	out := fmt.Sprintf("run #%d · total %s\n", int(get("run")), sec(get("total_ms")))
	out += fmt.Sprintf("  model %s (TTFT median %s, max %s) · tools %s · approvals %s · post-run %s\n",
		sec(get("model_ms")), sec(get("ttft_ms")), sec(get("ttft_max_ms")),
		sec(get("tool_ms")), sec(get("perm_wait_ms")), sec(get("post_ms")))
	out += fmt.Sprintf("  prompt %d (uncached %d, cached %d, %.0f%% cache hit) · out %d · reasoning %d · %.0f tok/s\n",
		int(get("in")+get("cached")), int(get("in")),
		int(get("cached")), cachePct(get("in"), get("cached")),
		int(get("out")), int(get("reasoning")), get("tps"))
	if iters, ok := stats["iters"].([]any); ok && len(iters) > 0 {
		out += fmt.Sprintf("  %d model call(s):\n", len(iters))
		for i, raw := range iters {
			it, _ := raw.(map[string]any)
			if it == nil {
				continue
			}
			if i >= 12 {
				out += fmt.Sprintf("  … %d more\n", len(iters)-i)
				break
			}
			out += fmt.Sprintf("    #%d ttft %s · %s · prompt %.1fk (cached %.0f%%) · out %d\n",
				int(numOf(it["iter"])), sec(numOf(it["ttft_ms"])), sec(numOf(it["duration_ms"])),
				(numOf(it["in"])+numOf(it["cached"]))/1000,
				cachePct(numOf(it["in"]), numOf(it["cached"])), int(numOf(it["out"])))
		}
	}
	if tools, ok := stats["tools"].([]any); ok && len(tools) > 0 {
		var total int
		for _, t := range tools {
			if tm, ok := t.(map[string]any); ok {
				total += int(numOf(tm["ms"]))
			}
		}
		out += fmt.Sprintf("  %d tool call(s), %s total\n", len(tools), sec(float64(total)))
	}
	return out
}

// cachePct is the share of prompt tokens served from the server cache. The
// provider reports uncached (In) and cache-read tokens separately, so the
// total prompt is their sum.
func cachePct(uncached, cached float64) float64 {
	total := uncached + cached
	if total <= 0 {
		return 0
	}
	return 100 * cached / total
}

func numOf(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

// ---- runner-facing helpers ------------------------------------------------

func (r *Runner) resetPermWait() {
	r.mu.Lock()
	r.permWait = 0
	r.mu.Unlock()
}

func (r *Runner) addPermWait(d time.Duration) {
	r.mu.Lock()
	r.permWait += d
	r.mu.Unlock()
}

func (r *Runner) takePermWait() time.Duration {
	r.mu.Lock()
	d := r.permWait
	r.permWait = 0
	r.mu.Unlock()
	return d
}

// LastPerf returns the most recent run's timing summary (nil before any run).
func (r *Runner) LastPerf() *perfSummary {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastPerf
}
