// Package app wires the fyagent core together: config, model monitor, and
// the UI bridge. Both the GUI and the headless CLI build on it.
package app

import (
	"context"
	"encoding/json"
	"time"

	"fyagent/core/config"
	"fyagent/core/model"
	"fyagent/core/uiapi"
)

const healthInterval = 10 * time.Second

type App struct {
	Cfg     *config.Config
	Monitor *model.Monitor
	Bridge  *uiapi.Bridge

	lastHealth string // dedupe provider.state events
}

func New() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, Bridge: uiapi.New()}
	a.Monitor = model.NewMonitor(cfg.Provider, a.onHealth)
	return a, nil
}

func (a *App) Start(ctx context.Context) {
	a.Monitor.Start(ctx, healthInterval)
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
