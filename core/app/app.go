// Package app wires the fyagent core together: config, model monitor, and
// the UI bridge. Both the GUI and the headless CLI build on it.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"fyagent/core/agent"
	"fyagent/core/config"
	"fyagent/core/harness"
	"fyagent/core/harnesslib"
	"fyagent/core/improve"
	"fyagent/core/journey"
	"fyagent/core/memory"
	"fyagent/core/model"
	"fyagent/core/paths"
	"fyagent/core/review"
	"fyagent/core/skills"
	"fyagent/core/tieindex"
	"fyagent/core/uiapi"

	"github.com/deepnoodle-ai/dive/experimental/mcp"
	"github.com/deepnoodle-ai/dive/llm"
)

const healthInterval = 10 * time.Second

type App struct {
	Cfg     *config.Config
	Monitor *model.Monitor
	Bridge  *uiapi.Bridge
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
	lastHealth     string // dedupe provider.state events
}

func New() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, Bridge: uiapi.New()}
	a.Monitor = model.NewMonitor(cfg.Provider, a.onHealth)
	wd, _ := os.Getwd()
	a.Runner, err = agent.NewRunner(cfg, a.Monitor, a.Bridge, wd)
	if err != nil {
		return nil, err
	}
	a.Tie = tieindex.New(cfg.Tie)
	a.Runner.SetIndex(a.Tie)
	a.Mem = memory.NewStore(cfg.Memory)
	a.Runner.SetMemory(a.Mem)
	a.Skills = skills.NewStore(cfg.Skills)
	a.Runner.SetSkills(a.Skills)
	a.Harness = harness.New(paths.Harness())
	if err := a.Harness.Seed(); err != nil {
		fmt.Fprintf(os.Stderr, "fyagent: harness seed: %v\n", err)
	}
	a.Runner.SetHarness(a.Harness)
	a.Libs = harnesslib.New(paths.HarnessLib())
	if err := a.Libs.Seed(); err != nil {
		fmt.Fprintf(os.Stderr, "fyagent: harness-lib seed: %v\n", err)
	}
	for _, w := range a.Harness.Validate(cfg.Harness.CharLimit) {
		fmt.Fprintf(os.Stderr, "fyagent: harness: %s\n", w)
	}
	a.Journey = journey.New()
	a.Bridge.SetTap(a.journeyTap)
	a.Review = review.NewManager(cfg.Review, cfg.Memory, cfg.Skills, a.Mem, a.Skills, a.Tie,
		a.Bridge.Emit,
		func() llm.LLM {
			h := a.Monitor.Current()
			return model.NewProvider(cfg.Provider, h.Model)
		})
	a.Review.Digest = func(ctx context.Context, sid string) string {
		return a.Runner.Digest(ctx, sid, cfg.Review.MaxInputTokens*4)
	}
	a.Review.SetActivityHooks(func() bool { return a.Runner.Busy() || a.ImproveBusy() }, a.Runner.LastActivity)
	a.Runner.SetReview(a.Review)
	return a, nil
}

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

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ConnectTie connects the tie index (blocking; call off the GUI path) and
// reports the result as a tie.state event. Safe to retry.
func (a *App) ConnectTie() {
	if !a.Tie.Enabled() {
		return
	}
	if err := a.Tie.Connect(); err != nil {
		a.Tie.LogState()
	} else {
		a.Tie.LogState()
	}
	a.Bridge.Emit(map[string]any{"type": "tie.state", "state": a.Tie.State()})
}

func (a *App) Start(ctx context.Context) {
	a.Monitor.Start(ctx, healthInterval)
	go a.ConnectTie() // never blocks the GUI; degrades gracefully
	if a.Cfg.Review.Enabled {
		go a.Review.Start(ctx)
	}
	go a.ConnectMCP(ctx)
}

// AgentAPI is the `agent` global exposed to Risor scripts.
func (a *App) AgentAPI() map[string]any {
	api := a.Bridge.API()
	api["provider"] = func() any { return toJSON(a.Monitor.Current()) }
	api["config"] = func() any { return toJSON(a.Cfg) }
	api["probe"] = func() any {
		a.Monitor.ProbeNow()
		return toJSON(a.Monitor.Current())
	}
	api["saveConfig"] = func(doc string) any {
		if err := a.Cfg.Apply([]byte(doc)); err != nil {
			return toJSON(map[string]any{"ok": false, "error": err.Error()})
		}
		a.Monitor.SetProvider(a.Cfg.Provider)
		a.Bridge.Emit(map[string]any{"type": "config.changed"})
		return toJSON(map[string]any{"ok": true})
	}
	api["send"] = func(input string) any {
		a.Runner.Send(input)
		return nil
	}
	api["interrupt"] = func() any {
		a.Runner.Interrupt()
		return nil
	}
	api["sessions"] = func() any { return a.Runner.SessionsJSON() }
	api["newSession"] = func() any {
		a.Runner.NewSession()
		return nil
	}
	api["openSession"] = func(id string) any {
		a.Runner.OpenSession(id)
		return nil
	}
	api["respondPerm"] = func(id, action string) any {
		a.Runner.RespondPerm(id, action)
		return nil
	}
	api["setMode"] = func(mode string) any {
		a.Runner.SetMode(mode)
		return nil
	}
	api["mode"] = func() any { return a.Runner.Mode() }
	api["tieState"] = func() any { return toJSON(a.Tie.State()) }
	api["memoryView"] = func() any {
		m, _ := a.Mem.Read("memory")
		u, _ := a.Mem.Read("user")
		return toJSON(map[string]any{
			"memory": m, "user": u,
			"memory_cap": a.Cfg.Memory.MemoryCharLimit,
			"user_cap":   a.Cfg.Memory.UserCharLimit,
		})
	}
	api["memoryPending"] = func() any {
		list, err := a.Mem.ListPending()
		if err != nil {
			return toJSON([]any{})
		}
		if list == nil {
			return toJSON([]any{})
		}
		return toJSON(list)
	}
	api["learnNow"] = func() any { a.Review.LearnNow(); return nil }
	api["improveStart"] = func() any {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
			defer cancel()
			if _, err := a.Improve(ctx); err != nil {
				a.Bridge.Emit(map[string]any{"type": "improve.error", "error": err.Error()})
			}
		}()
		return nil
	}
	api["harnessInfo"] = func() any { return toJSON(a.Runner.HarnessInfo()) }
	api["harnessLibList"] = func() any {
		ls := a.Libs.List()
		if ls == nil {
			return toJSON([]any{})
		}
		return toJSON(ls)
	}
	api["harnessFiles"] = func() any {
		fs := a.Harness.Files()
		if fs == nil {
			return toJSON([]string{})
		}
		return toJSON(fs)
	}
	api["harnessGet"] = func(name string) any {
		b, err := a.Harness.GetFile(name)
		return toJSON(map[string]any{"name": name, "content": b, "error": errString(err)})
	}
	api["harnessSave"] = func(name, content string) any {
		err := a.Harness.SaveFile(name, content)
		warns := a.Harness.Validate(a.Cfg.Harness.CharLimit)
		if warns == nil {
			warns = []string{}
		}
		a.Bridge.Emit(map[string]any{"type": "harness.warn", "warns": warns, "saved": name})
		return toJSON(map[string]any{"error": errString(err), "warns": warns,
			"restart_for_rules": true})
	}
	api["libExperience"] = func(id string) any {
		b, err := a.Libs.Experience(id)
		return toJSON(map[string]any{"id": id, "content": b, "error": errString(err)})
	}
	api["libPromote"] = func(id string) any {
		backup, err := a.Libs.Promote(id)
		warns := a.Harness.Validate(a.Cfg.Harness.CharLimit)
		if warns == nil {
			warns = []string{}
		}
		a.Bridge.Emit(map[string]any{"type": "harness.promoted", "entry": id,
			"backup": backup, "error": errString(err), "warns": warns})
		a.Journey.Append("harness-promote", map[string]any{"entry": id, "backup": backup})
		return toJSON(map[string]any{"entry": id, "backup": backup,
			"error": errString(err), "warns": warns})
	}
	api["journey"] = func(limit int) any {
		list := a.Journey.Tail(limit)
		if list == nil {
			return toJSON([]any{})
		}
		return toJSON(list)
	}
	api["skillsList"] = func() any { return toJSON(a.Skills.List()) }
	api["skillPending"] = func() any {
		list, err := a.Skills.ListPending()
		if err != nil || list == nil {
			return toJSON([]any{})
		}
		return toJSON(list)
	}
	api["respondSkill"] = func(id, decision string) any {
		approve := decision == "approve"
		op, result, err := a.Skills.Resolve(id, approve)
		out := map[string]any{"type": "skill.resolved", "id": id,
			"approved": approve, "error": "", "result": result}
		if op != nil {
			out["name"] = op.Name
			out["action"] = op.Action
		}
		if err != nil {
			out["error"] = err.Error()
		}
		a.Bridge.Emit(out)
		return nil
	}
	api["respondMemory"] = func(id, decision string) any {
		approve := decision == "approve"
		op, content, err := a.Mem.Resolve(id, approve)
		out := map[string]any{"type": "memory.resolved", "id": id,
			"approved": approve, "error": ""}
		if op != nil {
			out["target"] = op.Target
			out["action"] = op.Action
		}
		if err != nil {
			out["error"] = err.Error()
		} else if approve {
			a.Bridge.Emit(map[string]any{"type": "memory.updated",
				"target": out["target"], "action": out["action"], "content": content})
		}
		a.Bridge.Emit(out)
		return nil
	}
	api["rebuildIndex"] = func() any {
		go func() {
			n, err := a.Runner.RebuildIndex(context.Background())
			msg := map[string]any{"type": "tie.rebuilt", "sessions": n, "error": ""}
			if err != nil {
				msg["error"] = err.Error()
			}
			a.Bridge.Emit(msg)
		}()
		return nil
	}
	api["quit"] = func() any {
		go func() {
			time.Sleep(150 * time.Millisecond) // let pending events flush
			os.Exit(0)
		}()
		return nil
	}
	return api
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
	res, err := improve.Step(ctx, improve.Options{
		Cfg:            a.Cfg,
		Model:          func() llm.LLM { return model.NewProvider(a.Cfg.Provider, a.Monitor.Current().Model) },
		ModelID:        h.Model,
		Harness:        a.Harness,
		Workdir:        a.Runner.Cwd(),
		SessionID:      sid,
		Task:           task,
		RefDigest:      digest,
		Emit:           a.Bridge.Emit,
		Library:        lib,
		IndexFn:        indexFn,
		K:              k,
		Mode:           mode,
		TotalWallS:     a.Cfg.Improve.TotalWallS,
		MaxTotalTokens: a.Cfg.Improve.MaxTotalTokens,
		RunID:          resumeID,
		Resume:         resumeID != "",
	})
	if err == nil && res.Successor != "" {
		if oerr := a.Runner.SetSessionOverlay(res.Successor); oerr != nil {
			fmt.Fprintf(os.Stderr, "fyagent: overlay install: %v\n", oerr)
		}
	}
	return res, err
}

func (a *App) onHealth(h model.Health) {
	doc := toJSON(h)
	if doc == a.lastHealth {
		return
	}
	a.lastHealth = doc
	a.Bridge.Emit(map[string]any{"type": "provider.state", "health": h})
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
