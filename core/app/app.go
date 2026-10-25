// Package app wires the anneal core together: config, model monitor, and
// the UI bridge. Both the GUI and the headless CLI build on it.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/uidbz/anneal/core/agent"
	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/events"
	"github.com/uidbz/anneal/core/harness"
	"github.com/uidbz/anneal/core/harnesslib"
	"github.com/uidbz/anneal/core/improve"
	"github.com/uidbz/anneal/core/journey"
	"github.com/uidbz/anneal/core/memory"
	"github.com/uidbz/anneal/core/model"
	"github.com/uidbz/anneal/core/paths"
	"github.com/uidbz/anneal/core/review"
	"github.com/uidbz/anneal/core/skills"
	"github.com/uidbz/anneal/core/tieindex"

	"github.com/deepnoodle-ai/dive/experimental/mcp"
	"github.com/deepnoodle-ai/dive/llm"
)

const healthInterval = 10 * time.Second

type App struct {
	Cfg     *config.Config
	Monitor *model.Monitor
	Bus     *events.Bus
	Runner  *agent.Runner
	Tie     *tieindex.Indexer
	Mem     *memory.Store
	Skills  *skills.Store
	Harness *harness.Store
	Libs    *harnesslib.Store
	Review  *review.Manager
	Journey *journey.Log
	MCP     *mcp.Manager

	improveMu      sync.Mutex
	improveRunning bool
	healthMu       sync.Mutex // guards lastHealth (concurrent probes)
	lastHealth     string     // dedupe provider.state events
}

func New() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	a := &App{Cfg: cfg, Bus: events.New()}
	a.Monitor = model.NewMonitor(cfg.Provider, a.onHealth)
	a.Harness = harness.New(paths.Harness())
	if err := a.Harness.Seed(); err != nil {
		fmt.Fprintf(os.Stderr, "anneal: harness seed: %v\n", err)
	}
	for _, w := range a.Harness.Validate(cfg.Harness.CharLimit) {
		fmt.Fprintf(os.Stderr, "anneal: harness: %s\n", w)
	}
	a.Runner, err = agent.NewRunner(cfg, a.Monitor, a.Bus, wd, a.Harness)
	if err != nil {
		return nil, err
	}
	a.Tie = tieindex.New(cfg.Tie)
	a.Runner.SetIndex(a.Tie)
	a.Mem = memory.NewStore(&cfg.Memory)
	a.Runner.SetMemory(a.Mem)
	a.Skills = skills.NewStore(&cfg.Skills)
	a.Runner.SetSkills(a.Skills)
	a.Libs = harnesslib.New(paths.HarnessLib())
	if err := a.Libs.Seed(); err != nil {
		fmt.Fprintf(os.Stderr, "anneal: harness-lib seed: %v\n", err)
	}
	a.Journey = journey.New()
	a.Bus.AddTap(a.journeyTap)
	a.Review = review.NewManager(&cfg.Review, a.Mem, a.Skills, a.Tie,
		a.Bus.Emit,
		func() llm.LLM {
			h := a.Monitor.Current()
			return model.NewProvider(cfg.Provider, h.Model)
		})
	a.Review.Digest = func(ctx context.Context, sid string) string {
		return a.Runner.Digest(ctx, sid, cfg.Review.MaxInputTokens*4)
	}
	a.Review.SetFlagStore(review.NewFlagStore(paths.HarnessQueue()))
	a.Review.SetActivityHooks(func() bool { return a.Runner.Busy() || a.ImproveBusy() }, a.Runner.LastActivity)
	a.Runner.SetReview(a.Review)
	return a, nil
}

// ReplayState emits the resumed session/mode/todos to whatever UI consumer is
// attached now (the runner is constructed before the UI exists).
func (a *App) ReplayState() { a.Runner.ReplayState() }

// journeyTap appends self-improvement events to the journey log.
func (a *App) journeyTap(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	var e map[string]any
	if json.Unmarshal(data, &e) != nil {
		return
	}
	switch e["type"] {
	case "memory.updated":
		a.Journey.Append("memory-"+toStr(e["action"]), map[string]any{"target": toStr(e["target"])})
	case "memory.resolved":
		if e["approved"] == true && e["error"] == "" {
			a.Journey.Append("memory-"+toStr(e["action"]), map[string]any{"target": toStr(e["target"]), "staged": true})
		}
	case "skill.applied":
		a.Journey.Append("skill-"+toStr(e["action"]), map[string]any{"name": toStr(e["name"])})
	case "skill.resolved":
		if e["approved"] == true && e["error"] == "" {
			a.Journey.Append("skill-"+toStr(e["action"]), map[string]any{"name": toStr(e["name"]), "staged": true})
		}
	case "review.flag":
		a.Journey.Append("review-flag", map[string]any{"gap": toStr(e["gap"]), "session": toStr(e["session"])})
	case "review.done":
		if ledger, ok := e["ledger"].([]any); ok && len(ledger) > 0 {
			a.Journey.Append("review", map[string]any{"session": toStr(e["session"]), "lessons": len(ledger)})
		}
	case "improve.done":
		if e["ok"] == true {
			a.Journey.Append("improve", map[string]any{"run": toStr(e["run"]), "successor": e["successor"] == true})
		}
	}
}

func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ConnectTie connects the tie index (blocking; call off the GUI path) and
// reports the result as a tie.state event. Safe to retry.
func (a *App) ConnectTie() {
	if !a.Tie.Enabled() {
		return
	}
	_ = a.Tie.Connect() // failures are recorded as degraded state
	a.Tie.LogState()
	a.Bus.Emit(map[string]any{"type": "tie.state", "state": a.Tie.State()})
}

// Start starts the health monitor and review scheduler and connects tie/MCP
// in the background. Use StartBlocking when the caller can wait (headless).
func (a *App) Start(ctx context.Context) {
	a.startCore(ctx)
	go a.ConnectTie() // never blocks the GUI; degrades gracefully
	go a.ConnectMCP(ctx)
}

// StartBlocking is Start with synchronous tie/MCP connection (headless CLI).
func (a *App) StartBlocking(ctx context.Context) {
	a.startCore(ctx)
	a.ConnectTie()
	a.ConnectMCP(ctx)
}

func (a *App) startCore(ctx context.Context) {
	a.Monitor.Start(ctx, healthInterval)
	if a.Cfg.Review.Enabled {
		go a.Review.Start(ctx)
	}
}

// ImproveBusy reports whether an improvement step is running (GPU guard).
func (a *App) ImproveBusy() bool {
	a.improveMu.Lock()
	defer a.improveMu.Unlock()
	return a.improveRunning
}

// Improve runs one contrastive harness-improvement step for the LAST
// finished run (reference = that session) and installs the successor as a
// session overlay. Serialized; returns the step record.
func (a *App) Improve(ctx context.Context) (*improve.Result, error) {
	return a.ImproveWith(ctx, 0, "", "")
}

// ImproveWith runs the recursion with k/mode overrides (0/"" = config) and
// optional resume of a prior run id (completed iterations are skipped).
func (a *App) ImproveWith(ctx context.Context, kOverride int, modeOverride, resumeID string) (*improve.Result, error) {
	if a.Runner.Busy() {
		return nil, fmt.Errorf("improve: a foreground run is active")
	}
	a.improveMu.Lock()
	if a.improveRunning {
		a.improveMu.Unlock()
		return nil, fmt.Errorf("improve: already running")
	}
	a.improveRunning = true
	a.improveMu.Unlock()
	defer func() {
		a.improveMu.Lock()
		a.improveRunning = false
		a.improveMu.Unlock()
	}()

	sid := a.Runner.SessionID()
	task := a.Runner.LastTask()
	if task == "" {
		return nil, fmt.Errorf("improve: no completed task to improve")
	}
	if !a.Cfg.Improve.Enabled {
		return nil, fmt.Errorf("improve: disabled in config")
	}
	a.Review.Interrupt() // improvement owns the GPU next; foreground semantics
	digest := a.Runner.Digest(ctx, sid, a.Cfg.Review.MaxInputTokens*4)
	var lib *harnesslib.Store
	var indexFn func(string, string)
	if a.Cfg.Improve.Library {
		lib = a.Libs
		if a.Tie.Enabled() {
			indexFn = func(id, text string) { _ = a.Tie.IndexExperience(id, text) }
		}
	}
	h := a.Monitor.Current()
	k, mode := a.Cfg.Improve.K, a.Cfg.Improve.Mode
	if kOverride > 0 {
		k = kOverride
	}
	if modeOverride != "" {
		mode = modeOverride
	}
	// M16 review-loop bridge: reviewer-flagged deficiencies become hints
	// for this step's editors (evidence in the probe still decides).
	runid := resumeID
	if runid == "" {
		runid = time.Now().UTC().Format("20060102-150405")
	}
	var hints []string
	flags := a.Review.ConsumeFlags(runid, 6)
	for _, f := range flags {
		hints = append(hints, f.Gap)
	}
	res, err := improve.Step(ctx, improve.Options{
		Cfg:            a.Cfg,
		Model:          func() llm.LLM { return model.NewProvider(a.Cfg.Provider, a.Monitor.Current().Model) },
		ModelID:        h.Model,
		Harness:        a.Harness,
		Workdir:        a.Runner.Cwd(),
		SessionID:      sid,
		Task:           task,
		RefDigest:      digest,
		Emit:           a.Bus.Emit,
		Library:        lib,
		IndexFn:        indexFn,
		K:              k,
		Mode:           mode,
		TotalWallS:     a.Cfg.Improve.TotalWallS,
		MaxTotalTokens: a.Cfg.Improve.MaxTotalTokens,
		RunID:          runid,
		Resume:         resumeID != "",
		Hints:          hints,
	})
	if err != nil {
		// Hints were not applied; make them available to the next step.
		a.Review.ReleaseFlags(runid)
		return res, err
	}
	if res.Successor != "" {
		if oerr := a.Runner.SetSessionOverlay(res.Successor); oerr != nil {
			fmt.Fprintf(os.Stderr, "anneal: overlay install: %v\n", oerr)
		}
	}
	return res, nil
}

func (a *App) onHealth(h model.Health) {
	doc := toJSON(h)
	a.healthMu.Lock()
	if doc == a.lastHealth {
		a.healthMu.Unlock()
		return
	}
	a.lastHealth = doc
	a.healthMu.Unlock()
	a.Bus.Emit(map[string]any{"type": "provider.state", "health": h})
}

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":` + jsonErr(err) + `}`
	}
	return string(b)
}

func jsonErr(err error) string {
	b, e := json.Marshal(err.Error())
	if e != nil {
		return `"marshal error"`
	}
	return string(b)
}
