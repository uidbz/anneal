// Package app wires the fyagent core together: config, model monitor, and
// the UI bridge. Both the GUI and the headless CLI build on it.
package app

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"fyagent/core/agent"
	"fyagent/core/config"
	"fyagent/core/journey"
	"fyagent/core/memory"
	"fyagent/core/model"
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
	Review  *review.Manager
	Journey *journey.Log
	MCP     *mcp.Manager

	lastHealth string // dedupe provider.state events
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
	a.Review.SetActivityHooks(a.Runner.Busy, a.Runner.LastActivity)
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
