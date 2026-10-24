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
		"fyagent/core/memory"
	"fyagent/core/model"
	"fyagent/core/tieindex"
	"fyagent/core/uiapi"
)

const healthInterval = 10 * time.Second

type App struct {
	Cfg     *config.Config
	Monitor *model.Monitor
	Bridge  *uiapi.Bridge
	Runner  *agent.Runner
	Tie     *tieindex.Indexer
	Mem     *memory.Store

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
	return a, nil
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
