// remotebackend.go drives an anneald over the wire protocol (core/client).
// Reads the UI thread needs synchronously (sessions, harness overlay,
// improve capability) are cached and refreshed asynchronously; everything
// else is a direct RPC or fire-and-forget notification.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/uidbz/anneal/core/agent"
	"github.com/uidbz/anneal/core/client"
	"github.com/uidbz/anneal/core/events"
	"github.com/uidbz/anneal/core/improve"
	"github.com/uidbz/anneal/core/journey"
	"github.com/uidbz/anneal/core/skills"
)

// rpcTimeout bounds a remote call from the UI/pane thread. Localhost RPCs are
// sub-millisecond; the bound only bites when a backend is wedged.
const rpcTimeout = 10 * time.Second

type remoteBackend struct {
	c    *client.Client
	name string
	addr string

	mu        sync.Mutex
	sess      []agent.SessionInfo
	sessBusy  bool
	overlay   bool
	curMode   string
	curBusy   bool
	improveOn bool // improve.enabled from the backend config
	bus       *events.Bus

	refreshMu sync.Mutex
	lastSess  time.Time
}

// dialRemote connects to addr (unix:/path, host:port, or ws:// URL). token ""
// = no auth frame.
func dialRemote(ctx context.Context, name, addr, token string) (*remoteBackend, error) {
	c, err := client.Dial(ctx, addr, token)
	if err != nil {
		return nil, err
	}
	rb := &remoteBackend{c: c, name: name, addr: addr}
	// Seed the caches the shell reads before the first event lands.
	var info struct {
		Mode string `json:"mode"`
		Busy bool   `json:"busy"`
	}
	if err := rb.callInto("server.info", nil, &info); err == nil {
		rb.curMode, rb.curBusy = info.Mode, info.Busy
	}
	rb.refreshSessions()
	rb.refreshConfig()
	return rb, nil
}

func (r *remoteBackend) call(method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	return r.c.Call(ctx, method, params)
}

func (r *remoteBackend) callInto(method string, params any, out any) error {
	raw, err := r.call(method, params)
	if err != nil {
		return err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (r *remoteBackend) fire(method string, params any) {
	go func() { _, _ = r.call(method, params) }()
}

// attach forwards server notifications into the shell bus, and piggybacks
// cache refreshes on the events that invalidate them. When the connection
// dies, a backend.disconnected event lets the shell tell the user.
func (r *remoteBackend) attach(bus *events.Bus) {
	r.bus = bus
	go func() {
		for ev := range r.c.Events() {
			bus.Emit(map[string]any(ev))
			switch ev.Type() {
			case "session.started", "session.opened", "run.finished":
				r.refreshSessions()
				r.mu.Lock()
				r.curBusy = false
				r.mu.Unlock()
			case "harness.overlay":
				r.refreshOverlay()
			case "mode.changed":
				if m, ok := ev["mode"].(string); ok {
					r.mu.Lock()
					r.curMode = m
					r.mu.Unlock()
				}
			case "run.started":
				r.mu.Lock()
				r.curBusy = true
				r.mu.Unlock()
			case "config.changed":
				r.refreshConfig()
			}
		}
		bus.Emit(map[string]any{"type": "backend.disconnected", "backend": r.describe()})
	}()
}

func (r *remoteBackend) start(context.Context) {} // the server owns the core
func (r *remoteBackend) describe() string {
	if r.name != "" {
		return r.name
	}
	return r.addr
}
func (r *remoteBackend) remote() bool { return true }
func (r *remoteBackend) quit()        { r.fire("server.quit", map[string]any{"reason": "client"}) }

func (r *remoteBackend) send(t string) {
	go func() {
		var res struct {
			ID int  `json:"id"`
			OK bool `json:"ok"`
		}
		if err := r.callInto("agent.send", map[string]any{"text": t}, &res); err != nil {
			// No run.started/finished will arrive for a rejected send.
			if r.bus != nil {
				r.bus.Emit(map[string]any{"type": "run.finished", "ok": false,
					"error": "send failed: " + err.Error()})
			}
		}
	}()
}

func (r *remoteBackend) interrupt()            { r.fire("agent.interrupt", nil) }
func (r *remoteBackend) newSession()           { r.fire("sessions.new", nil) }
func (r *remoteBackend) openSession(id string) { r.fire("sessions.open", map[string]any{"id": id}) }
func (r *remoteBackend) respondPerm(id, act string) {
	r.fire("perm.respond", map[string]any{"id": id, "action": act})
}
func (r *remoteBackend) learnNow()     { r.fire("learn.now", nil) }
func (r *remoteBackend) improveStart() { r.fire("improve.start", nil) }
func (r *remoteBackend) rebuildIndex() { r.fire("index.rebuild", nil) }
func (r *remoteBackend) respondMemory(id, d string) {
	r.fire("memory.respond", map[string]any{"id": id, "action": d})
}
func (r *remoteBackend) respondSkill(id, d string) {
	r.fire("skill.respond", map[string]any{"id": id, "action": d})
}

func (r *remoteBackend) setMode(m string) {
	go func() {
		var res struct {
			Mode string `json:"mode"`
		}
		if err := r.callInto("mode.set", map[string]any{"mode": m}, &res); err == nil && res.Mode != "" {
			r.mu.Lock()
			r.curMode = res.Mode
			r.mu.Unlock()
		}
	}()
}

func (r *remoteBackend) saveConfig(doc string) error {
	_, err := r.call("config.set", map[string]any{"doc": doc})
	if err == nil {
		r.refreshConfig()
	}
	return err
}

func (r *remoteBackend) sessions() []agent.SessionInfo {
	r.mu.Lock()
	out := append([]agent.SessionInfo(nil), r.sess...)
	r.mu.Unlock()
	r.refreshSessions() // debounced: keep the list fresh for the next repaint
	return out
}

func (r *remoteBackend) cwd() string {
	var info struct {
		Workdir string `json:"workdir"`
	}
	if err := r.callInto("server.info", nil, &info); err == nil {
		return info.Workdir
	}
	return ""
}

func (r *remoteBackend) mode() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.curMode
}

func (r *remoteBackend) busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.curBusy
}

func (r *remoteBackend) improveEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.improveOn
}

func (r *remoteBackend) configDoc() (string, error) {
	raw, err := r.call("config.get", nil)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (r *remoteBackend) memoryView() (map[string]any, error) {
	var out map[string]any
	err := r.callInto("memory.view", nil, &out)
	return out, err
}

func (r *remoteBackend) skillsList() ([]skills.Info, error) {
	var out []skills.Info
	err := r.callInto("skills.list", nil, &out)
	return out, err
}

func (r *remoteBackend) journeyTail(n int) ([]journey.Entry, error) {
	var out []journey.Entry
	err := r.callInto("journey.tail", map[string]any{"n": n}, &out)
	return out, err
}

func (r *remoteBackend) improvementRuns(n int) ([]improve.Result, error) {
	var out []improve.Result
	err := r.callInto("improve.runs", map[string]any{"n": n}, &out)
	return out, err
}

func (r *remoteBackend) harnessOverlay() bool {
	r.mu.Lock()
	out := r.overlay
	r.mu.Unlock()
	r.refreshOverlay()
	return out
}

// paneAPI mirrors App.HarnessAPI over RPC; every call returns the same JSON
// strings the local pane API returns, so harness.risor is unchanged.
func (r *remoteBackend) paneAPI() map[string]any {
	rpc := func(method string, params any) any {
		raw, err := r.call(method, params)
		if err != nil {
			return toJSONString(map[string]any{"error": err.Error()})
		}
		return string(raw)
	}
	return map[string]any{
		"harnessInfo":  func() any { return rpc("harness.info", nil) },
		"harnessFiles": func() any { return rpc("harness.files", nil) },
		"harnessGet":   func(name string) any { return rpc("harness.get", map[string]any{"id": name}) },
		"harnessSave": func(name, content string) any {
			return rpc("harness.save", map[string]any{"name": name, "content": content})
		},
		"harnessLibList": func() any { return rpc("harness.lib", nil) },
		"libExperience":  func(id string) any { return rpc("harness.lib_experience", map[string]any{"id": id}) },
		"libPromote":     func(id string) any { return rpc("harness.lib_promote", map[string]any{"id": id}) },
		"flagPeek":       func(n int) any { return rpc("flags.peek", map[string]any{"n": n}) },
		"journey":        func(limit int) any { return rpc("journey.tail", map[string]any{"n": limit}) },
	}
}

// refreshSessions pulls sessions.list, debounced so a repaint storm cannot
// fan out RPCs.
func (r *remoteBackend) refreshSessions() {
	r.refreshMu.Lock()
	if time.Since(r.lastSess) < 2*time.Second {
		r.refreshMu.Unlock()
		return
	}
	r.lastSess = time.Now()
	r.refreshMu.Unlock()

	r.mu.Lock()
	if r.sessBusy {
		r.mu.Unlock()
		return
	}
	r.sessBusy = true
	r.mu.Unlock()

	go func() {
		var list []agent.SessionInfo
		err := r.callInto("sessions.list", nil, &list)
		r.mu.Lock()
		r.sessBusy = false
		if err == nil {
			r.sess = list
		}
		r.mu.Unlock()
	}()
}

// refreshOverlay pulls harness.info and looks for overlay-mounted components
// (named "overlay:…" by the runner).
func (r *remoteBackend) refreshOverlay() {
	go func() {
		var info struct {
			Components []struct {
				Name string `json:"name"`
			} `json:"components"`
		}
		if err := r.callInto("harness.info", nil, &info); err != nil {
			return
		}
		ovl := false
		for _, c := range info.Components {
			if len(c.Name) >= 8 && c.Name[:8] == "overlay:" {
				ovl = true
				break
			}
		}
		r.mu.Lock()
		r.overlay = ovl
		r.mu.Unlock()
	}()
}

// refreshConfig pulls config.get on change and extracts the capability flags
// the shell needs (improve.enabled).
func (r *remoteBackend) refreshConfig() {
	go func() {
		var doc struct {
			Improve struct {
				Enabled bool `json:"enabled"`
			} `json:"improve"`
		}
		if err := r.callInto("config.get", nil, &doc); err != nil {
			return
		}
		r.mu.Lock()
		r.improveOn = doc.Improve.Enabled
		r.mu.Unlock()
	}()
}

func toJSONString(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}
