// backend.go abstracts "drive the core" for the GUI shell: the local App
// (embedded core) or a remote anneald over the wire protocol (core/client).
// It mirrors the headless CLI's backend pattern so one shell implementation
// serves both modes. Commands are fire-and-forget where the core is; queries
// the UI thread needs synchronously are cached by the remote backend and
// refreshed asynchronously.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/uidbz/anneal/core/agent"
	coreapp "github.com/uidbz/anneal/core/app"
	"github.com/uidbz/anneal/core/events"
	"github.com/uidbz/anneal/core/improve"
	"github.com/uidbz/anneal/core/journey"
	"github.com/uidbz/anneal/core/paths"
	"github.com/uidbz/anneal/core/skills"
)

// backend is the shell's only handle on the core. attach forwards the core's
// events into the shell bus (which the event consumer and the pane bridge
// both read); the command/query methods replace the shell's direct
// App/Runner calls.
type backend interface {
	attach(bus *events.Bus)
	start(ctx context.Context)
	describe() string // "local" or the backend name/addr
	remote() bool     // remote backends own their config/state; the GUI is a thin client
	quit()

	// commands
	send(text string)
	interrupt()
	newSession()
	openSession(id string)
	respondPerm(id, action string)
	setMode(mode string)
	learnNow()
	improveStart()
	rebuildIndex()
	respondMemory(id, decision string)
	respondSkill(id, decision string)
	saveConfig(doc string) error

	// queries (must not block the UI thread for long)
	sessions() []agent.SessionInfo
	cwd() string
	mode() string
	busy() bool
	configDoc() (string, error)
	memoryView() (map[string]any, error)
	skillsList() ([]skills.Info, error)
	journeyTail(n int) ([]journey.Entry, error)
	improvementRuns(n int) ([]improve.Result, error)
	harnessOverlay() bool
	paneAPI() map[string]any
	improveEnabled() bool
}

// ---- local backend -------------------------------------------------------

type localBackend struct{ a *coreapp.App }

func (l *localBackend) attach(bus *events.Bus) { l.a.Bus.AddTap(bus.Emit) }
func (l *localBackend) start(ctx context.Context) {
	l.a.Start(ctx)
	l.a.ReplayState() // idempotent state replay after the tap exists
}
func (l *localBackend) describe() string      { return "local" }
func (l *localBackend) remote() bool          { return false }
func (l *localBackend) quit()                 {}
func (l *localBackend) send(t string)         { l.a.Runner.Send(t) }
func (l *localBackend) interrupt()            { l.a.Runner.Interrupt() }
func (l *localBackend) newSession()           { l.a.Runner.NewSession() }
func (l *localBackend) openSession(id string) { l.a.Runner.OpenSession(id) }
func (l *localBackend) respondPerm(id, act string) {
	l.a.Runner.RespondPerm(id, act)
}
func (l *localBackend) setMode(m string) { l.a.Runner.SetMode(m) }
func (l *localBackend) learnNow()        { l.a.Review.LearnNow() }
func (l *localBackend) improveStart() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
		defer cancel()
		if _, err := l.a.Improve(ctx); err != nil {
			l.a.Bus.Emit(map[string]any{"type": "improve.error", "error": err.Error()})
		}
	}()
}
func (l *localBackend) rebuildIndex() {
	go func() {
		n, err := l.a.Runner.RebuildIndex(context.Background())
		msg := map[string]any{"type": "tie.rebuilt", "sessions": n, "error": ""}
		if err != nil {
			msg["error"] = err.Error()
		}
		l.a.Bus.Emit(msg)
	}()
}
func (l *localBackend) respondMemory(id, d string)  { l.a.RespondMemory(id, d) }
func (l *localBackend) respondSkill(id, d string)   { l.a.RespondSkill(id, d) }
func (l *localBackend) saveConfig(doc string) error { return l.a.SaveConfig(doc) }
func (l *localBackend) sessions() []agent.SessionInfo {
	return l.a.Runner.Sessions()
}
func (l *localBackend) cwd() string          { return l.a.Runner.Cwd() }
func (l *localBackend) mode() string         { return l.a.Runner.Mode() }
func (l *localBackend) busy() bool           { return l.a.Runner.Busy() }
func (l *localBackend) improveEnabled() bool { return l.a.Cfg.Improve.Enabled }

func (l *localBackend) configDoc() (string, error) {
	b, err := json.MarshalIndent(l.a.Cfg, "", "  ")
	return string(b), err
}

func (l *localBackend) memoryView() (map[string]any, error) {
	m, _ := l.a.Mem.Read("memory")
	u, _ := l.a.Mem.Read("user")
	return map[string]any{
		"memory": m, "user": u,
		"memory_cap": l.a.Cfg.Memory.MemoryCharLimit,
		"user_cap":   l.a.Cfg.Memory.UserCharLimit,
	}, nil
}

func (l *localBackend) skillsList() ([]skills.Info, error) { return l.a.Skills.List(), nil }

func (l *localBackend) journeyTail(n int) ([]journey.Entry, error) {
	return l.a.Journey.Tail(n), nil
}

func (l *localBackend) improvementRuns(n int) ([]improve.Result, error) {
	return improve.LoadRuns(paths.Improvements(), n), nil
}

func (l *localBackend) harnessOverlay() bool {
	sid := l.a.Runner.SessionID()
	if sid == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(paths.Overlays(), sid))
	return err == nil && st.IsDir()
}

func (l *localBackend) paneAPI() map[string]any { return l.a.HarnessAPI() }
