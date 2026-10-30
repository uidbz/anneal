// Package server exposes the anneal core over the network (docs/PROTOCOL.md):
// JSON-RPC 2.0 over WebSocket for commands, plus the raw core/events bus fan-out
// as server->client notifications. It is the backend half of the
// backend/frontend split; cmd/anneald hosts it in a (typically containerized)
// headless process.
//
// Design rules inherited from the core:
//   - the events.Bus is the only up-stream; this package adds exactly one tap
//     and fans out per connection (taps must never block — slow clients are
//     disconnected, mirroring the GUI/headless channel pattern);
//   - every command routes through the same App/Runner methods the GUI calls;
//   - idle shutdown keeps the ephemeral-container model: no client frames and
//     no run activity for idle_timeout_s => clean exit.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/uidbz/anneal/core/app"
	"github.com/uidbz/anneal/core/config"

	"github.com/gorilla/websocket"
)

// ProtoVersion is the wire protocol revision. Clients send their revision in
// the hello frame; the server answers with its own. Additive changes do not
// bump it; framing/semantic breaks do.
const ProtoVersion = 1

// connQueue is the per-connection event buffer. It matches the GUI/headless
// tap channels; a connection that fills it is closed (slow consumer).
const connQueue = 1024

type conn struct {
	srv  *Server
	ws   *websocket.Conn
	send chan []byte // pre-marshalled event frames
	die  chan struct{}
	once sync.Once
}

func (c *conn) close() { c.once.Do(func() { close(c.die); c.ws.Close() }) }

// Server hosts the protocol on one App instance.
type Server struct {
	App     *app.App
	Version string
	cfg     config.Server
	token   string // resolved from cfg.AuthTokenEnv; "" = no auth

	up      time.Time
	mu      sync.Mutex
	conns   map[*conn]struct{}
	addrs   []string  // bound addresses after Run (listener order)
	lastAct time.Time // last client frame (any connection), feeds the idle timer

	idleTimerFired bool
	shutdownOnce   sync.Once
	reason         string
	cancel         context.CancelFunc // set by Run
	lastBusy       time.Time          // run/improve/review edges; defers idle exit

	upgrader websocket.Upgrader
}

// New builds a server from the app config's server section. The auth token is
// read from the env var named by auth_token_env (empty value = auth disabled).
func New(a *app.App, version string) (*Server, error) {
	cfg := a.Cfg.Server
	s := &Server{
		App:     a,
		Version: version,
		cfg:     cfg,
		conns:   map[*conn]struct{}{},
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// Same-origin policy is meaningless for local/desktop/WS clients;
			// authentication is the hello-frame token.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
	if cfg.AuthTokenEnv != "" {
		s.token = os.Getenv(cfg.AuthTokenEnv)
	}
	return s, nil
}

// Run opens every configured listener, serves until ctx is cancelled or the
// idle timer / server.quit triggers shutdown, then drains and returns. The
// shutdown reason is available via Reason().
func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.up = time.Now()
	s.lastAct = time.Now()
	defer cancel()

	s.App.Bus.AddTap(s.fanout)
	// Busy edges (run/improve/review start AND finish) count as activity:
	// an idle window opened by a long-running protected job must not expire
	// the instant that job completes (PROTOCOL.md lifecycle).
	s.App.Bus.AddTap(func(v any) {
		if m, ok := v.(map[string]any); ok {
			switch m["type"] {
			case "run.started", "run.finished", "improve.start", "improve.done",
				"improve.error", "review.start", "review.done":
				s.mu.Lock()
				s.lastBusy = time.Now()
				s.mu.Unlock()
			}
		}
	})

	var lnss []net.Listener
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"version":%q,"reason":%q}`, s.Version, s.Reason())
	})
	httpSrv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	for _, addr := range s.cfg.Listen {
		if s.token == "" && !tokenlessBindOK(addr) {
			return fmt.Errorf("anneald: refusing to bind %s without a token (set the %s env var)", addr, s.cfg.AuthTokenEnv)
		}
		ln, err := listen(addr)
		if err != nil {
			for _, l := range lnss {
				l.Close()
			}
			return fmt.Errorf("anneald: listen %s: %w", addr, err)
		}
		lnss = append(lnss, ln)
		s.mu.Lock()
		s.addrs = append(s.addrs, ln.Addr().String())
		s.mu.Unlock()
		fmt.Fprintf(os.Stderr, "anneald: listening on %s (%s)\n", addr, ln.Addr())
		go func(ln net.Listener) {
			_ = httpSrv.Serve(ln) // returns when the listener closes
		}(ln)
	}
	go s.idleLoop(ctx)

	<-ctx.Done()
	s.closeConns() // drain window: events still flow until here
	httpSrv.Close()
	for _, ln := range lnss {
		ln.Close()
	}
	return nil
}

// Reason reports why shutdown started ("" = still running).
func (s *Server) Reason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason
}

// Addrs returns the bound listener addresses (valid after Run has opened
// them; useful for tests dialing port 0).
func (s *Server) Addrs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.addrs...)
}

// beginShutdown announces the shutdown on the bus, then closes listeners
// after the drain grace. Idempotent.
func (s *Server) beginShutdown(reason string) {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.reason = reason
		grace := time.Duration(s.cfg.DrainGraceS) * time.Second
		s.mu.Unlock()
		s.App.Bus.Emit(map[string]any{
			"type": "server.shutting_down", "reason": reason, "eta_s": int(grace.Seconds()),
		})
		time.AfterFunc(grace, func() {
			if s.cancel != nil {
				s.cancel()
			}
		})
	})
}

// idleLoop implements the ephemeral lifecycle: with no client frame and no
// run/improve activity for idle_timeout_s the server shuts itself down.
// A run in flight always defers the timer (never kill mid-run).
func (s *Server) idleLoop(ctx context.Context) {
	if s.cfg.IdleTimeoutS <= 0 {
		return
	}
	idle := time.Duration(s.cfg.IdleTimeoutS) * time.Second
	tick := idle / 4
	if tick > 5*time.Second {
		tick = 5 * time.Second
	}
	if tick < 500*time.Millisecond {
		tick = 500 * time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.App.Runner.Busy() || s.App.ImproveBusy() {
				continue
			}
			s.mu.Lock()
			last := s.lastAct
			if s.lastBusy.After(last) {
				last = s.lastBusy
			}
			s.mu.Unlock()
			if act := s.App.Runner.LastActivity(); act.After(last) {
				last = act
			}
			if time.Since(last) >= idle {
				s.mu.Lock()
				fired := s.idleTimerFired
				s.idleTimerFired = true
				s.mu.Unlock()
				if !fired {
					fmt.Fprintf(os.Stderr, "anneald: idle > %v, shutting down\n", idle)
					s.beginShutdown("idle")
				}
			}
		}
	}
}

func (s *Server) touch() {
	s.mu.Lock()
	s.lastAct = time.Now()
	s.mu.Unlock()
}

// fanout is the single bus tap: marshal once, queue per connection. Blocking
// a tap is forbidden by the bus contract, so a full queue kills the client.
// With no connections it returns before marshalling — the tap outlives Run
// (the bus has no unsubscribe), and closeConns empties the map.
func (s *Server) fanout(v any) {
	s.mu.Lock()
	if len(s.conns) == 0 {
		s.mu.Unlock()
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		s.mu.Unlock()
		return
	}
	var dead []*conn
	for c := range s.conns {
		select {
		case c.send <- data:
		default:
			dead = append(dead, c) // slow consumer
		}
	}
	s.mu.Unlock()
	for _, c := range dead {
		c.close()
	}
}

func (s *Server) closeConns() {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.conns = map[*conn]struct{}{} // stop fanout work for dead connections
	s.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
}

// listen accepts "unix:/path" and "host:port" (TCP).
func listen(addr string) (net.Listener, error) {
	if path := trimUnix(addr); path != "" {
		_ = os.Remove(path) // stale socket from a previous run
		ln, err := net.Listen("unix", path)
		if err != nil {
			return nil, err
		}
		// The socket lives in the bind-mounted state dir: keep it owner-only.
		if err := os.Chmod(path, 0o600); err != nil {
			ln.Close()
			return nil, err
		}
		return ln, nil
	}
	return net.Listen("tcp", addr)
}

func trimUnix(addr string) string {
	if len(addr) >= 5 && addr[:5] == "unix:" {
		return addr[5:]
	}
	return ""
}

// tokenlessBindOK reports whether an address may be bound without a token:
// Unix sockets (owner-only 0600) and loopback TCP only. Wildcard or remote
// binds require auth.
func tokenlessBindOK(addr string) bool {
	if trimUnix(addr) != "" {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkToken compares the hello-frame bearer token against the configured
// one (constant-time; empty configured token = auth disabled).
func (s *Server) checkToken(got string) bool {
	if s.token == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(s.token), []byte(got)) == 1
}
