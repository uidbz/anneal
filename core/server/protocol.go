// Wire protocol (docs/PROTOCOL.md): after the hello handshake the client
// sends JSON-RPC 2.0 requests; the server answers with responses and pushes
// raw bus events (the same JSON objects the GUI/headless sinks consume) as
// unsolicited notifications. Frames carrying "jsonrpc" are RPC; frames
// carrying "type" are events.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait    = 10 * time.Second
	helloTimeout = 10 * time.Second
	maxFrame     = 1 << 20 // 1 MiB inbound frames

	// Keepalive (PROTOCOL.md lifecycle): the server pings every pingPeriod;
	// a connection that misses pongs for pongWait is reaped.
	pingPeriod = 30 * time.Second
	pongWait   = 75 * time.Second
)

// JSON-RPC error codes (spec range + -32000 handler errors, -32001 shutting).
const (
	codeParse     = -32700
	codeInvalid   = -32600
	codeNoMethod  = -32601
	codeBadParams = -32602
	codeHandler   = -32000
	codeShutting  = -32001
)

type helloFrame struct {
	Type   string `json:"type"`
	Proto  int    `json:"proto"`
	Token  string `json:"token"`
	Client string `json:"client"`
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// handler dispatches one RPC. params is the raw JSON object (may be nil).
type handler func(params json.RawMessage) (any, error)

func argErr(err error) error { return &wrapped{codeBadParams, err} }

type wrapped struct {
	code int
	err  error
}

func (w *wrapped) Error() string { return w.err.Error() }

// decode unmarshals params into v; missing params is an error for handlers
// that need an argument.
func decode(params json.RawMessage, v any) error {
	if len(params) == 0 {
		return argErr(fmt.Errorf("missing params"))
	}
	if err := json.Unmarshal(params, v); err != nil {
		return argErr(err)
	}
	return nil
}

// handleWS upgrades the connection, performs the hello handshake, then serves
// RPC until either side closes.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// Browsers always attach Origin to a WebSocket handshake; native
	// clients never do. With auth disabled, refuse the browser surface:
	// a random web page in the user's own browser must not drive the
	// agent over a tokenless loopback TCP listener.
	if s.token == "" && r.Header.Get("Origin") != "" {
		http.Error(w, `{"error":"origin not allowed without a token"}`, http.StatusForbidden)
		return
	}
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already closed the socket
	}
	ws.SetReadLimit(maxFrame)

	// Hello frame first: protocol revision + bearer token.
	_ = ws.SetReadDeadline(time.Now().Add(helloTimeout))
	var h helloFrame
	if err := ws.ReadJSON(&h); err == nil {
		if h.Type == "hello" && h.Proto == ProtoVersion && s.checkToken(h.Token) {
			s.serve(ws, &h)
			return
		}
	}
	reject := map[string]any{"type": "hello.reject", "proto": ProtoVersion}
	if h.Proto > 0 && h.Proto != ProtoVersion {
		reject["error"] = fmt.Sprintf("unsupported proto %d (server %d)", h.Proto, ProtoVersion)
	} else {
		reject["error"] = "hello rejected"
	}
	_ = ws.WriteJSON(reject)
	ws.Close()
}

// serve owns one authenticated connection: writer goroutine, RPC read loop.
func (s *Server) serve(ws *websocket.Conn, h *helloFrame) {
	// The hello deadline must not survive the handshake; from here liveness
	// is defined by the ping/pong keepalive, not frame traffic.
	ws.SetReadDeadline(time.Now().Add(pongWait))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	c := &conn{srv: s, ws: ws, send: make(chan []byte, connQueue), die: make(chan struct{})}

	s.mu.Lock()
	if s.reason != "" { // raced with shutdown: refuse instead of a silent close
		reason := s.reason
		s.mu.Unlock()
		_ = ws.WriteJSON(map[string]any{"type": "hello.reject", "proto": ProtoVersion,
			"error": "server is shutting down (" + reason + ")"})
		ws.Close()
		return
	}
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	// Cleanup installed before anything else can fail on this path.
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		c.close()
	}()

	_ = ws.WriteJSON(map[string]any{
		"type": "hello.ack", "proto": ProtoVersion, "server": s.Version,
		"idle_timeout_s": s.cfg.IdleTimeoutS, "drain_grace_s": s.cfg.DrainGraceS,
	})

	go c.writeLoop()
	s.App.ReplayState() // idempotent state replay: session.opened/mode/todo

	methods := s.methods()
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		s.touch()
		s.dispatch(c, methods, data)
	}
}

// writeLoop drains the event queue, forwards RPC replies, and pings. It is
// the only writer on the socket (WriteControl included). Any write error
// kills the connection (the read loop's defer cleans up).
func (c *conn) writeLoop() {
	tick := time.NewTicker(pingPeriod)
	defer tick.Stop()
	for {
		select {
		case <-c.die:
			_ = c.ws.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			return
		case data := <-c.send:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(websocket.TextMessage, data); err != nil {
				c.close()
				return
			}
		case <-tick.C:
			if err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				c.close()
				return
			}
		}
	}
}

// dispatch parses one frame and answers it. Per spec, notifications (no id)
// never receive a reply; their failures surface as events. Parse errors are
// replyable with id null because the id cannot be extracted.
func (s *Server) dispatch(c *conn, methods map[string]handler, data []byte) {
	var r rpcReq
	if err := json.Unmarshal(data, &r); err != nil {
		s.reply(c, json.RawMessage("null"), &rpcErr{codeParse, "invalid request"})
		return
	}
	notif := isNotification(r.ID)
	if r.JSONRPC != "2.0" || r.Method == "" {
		if !notif {
			s.reply(c, r.ID, &rpcErr{codeInvalid, "request must carry jsonrpc 2.0 and a method"})
		}
		return
	}
	s.mu.Lock()
	shutting := s.reason != ""
	s.mu.Unlock()
	if shutting && r.Method != "server.quit" {
		if !notif {
			s.reply(c, r.ID, &rpcErr{codeShutting, "server is shutting down"})
		}
		return
	}
	h, ok := methods[r.Method]
	if !ok {
		if !notif {
			s.reply(c, r.ID, &rpcErr{codeNoMethod, "unknown method: " + r.Method})
		}
		return
	}
	result, err := h(r.Params)
	if notif {
		return // notifications get no reply, errors surface as events
	}
	if err != nil {
		code := codeHandler
		if w, ok := err.(*wrapped); ok {
			code = w.code
		}
		s.reply(c, r.ID, &rpcErr{code, err.Error()})
		return
	}
	s.reply(c, r.ID, result)
}

func isNotification(id json.RawMessage) bool {
	return len(id) == 0 || string(id) == "null"
}

func (s *Server) reply(c *conn, id json.RawMessage, result any) {
	frame := map[string]any{"jsonrpc": "2.0"}
	if len(id) > 0 {
		frame["id"] = json.RawMessage(id)
	}
	switch v := result.(type) {
	case *rpcErr:
		frame["error"] = v
	default:
		frame["result"] = v
	}
	data, err := json.Marshal(frame)
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	default:
		c.close() // slow consumer
	}
}

// ---- param shapes -------------------------------------------------------

type pText struct {
	Text string `json:"text"`
}
type pID struct {
	ID string `json:"id"`
}
type pIDAction struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}
type pMode struct {
	Mode string `json:"mode"`
}
type pDoc struct {
	Doc string `json:"doc"`
}
type pNameContent struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}
type pN struct {
	N int `json:"n"`
}
type pImprove struct {
	K      int    `json:"k"`
	Mode   string `json:"mode"`
	Resume string `json:"resume"`
}
type pQuit struct {
	Reason string `json:"reason"`
}

// ---- the method table ---------------------------------------------------

// methods builds the RPC surface: one handler per capability the local GUI
// has on App/Runner (the AgentAPI surface), named with dotted namespaces.
func (s *Server) methods() map[string]handler {
	a := s.App
	m := map[string]handler{}

	// server
	m["server.ping"] = func(json.RawMessage) (any, error) {
		return map[string]any{"ok": true}, nil
	}
	m["server.info"] = func(json.RawMessage) (any, error) {
		return map[string]any{
			"version": s.Version, "proto": ProtoVersion,
			"workdir": a.Runner.Cwd(), "mode": a.Runner.Mode(),
			"session": a.Runner.SessionID(), "busy": a.Runner.Busy(),
			"idle_timeout_s": s.cfg.IdleTimeoutS,
			"uptime_s":       int(time.Since(s.up).Seconds()),
		}, nil
	}
	m["server.quit"] = func(params json.RawMessage) (any, error) {
		var p pQuit
		reason := "client"
		if len(params) > 0 && json.Unmarshal(params, &p) == nil && p.Reason != "" {
			reason = p.Reason
		}
		s.beginShutdown(reason)
		return map[string]any{"ok": true}, nil
	}

	// runs
	m["agent.send"] = func(params json.RawMessage) (any, error) {
		var p pText
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if p.Text == "" {
			return nil, argErr(fmt.Errorf("text is empty"))
		}
		id, ok := a.Runner.StartTask(p.Text)
		return map[string]any{"id": id, "ok": ok}, nil
	}
	m["agent.interrupt"] = func(json.RawMessage) (any, error) {
		a.Runner.Interrupt()
		return nil, nil
	}
	m["run.status"] = func(json.RawMessage) (any, error) {
		return map[string]any{"busy": a.Runner.Busy(), "mode": a.Runner.Mode(),
			"session": a.Runner.SessionID()}, nil
	}

	// sessions
	m["sessions.list"] = func(json.RawMessage) (any, error) {
		return json.RawMessage(a.Runner.SessionsJSON()), nil
	}
	m["sessions.new"] = func(json.RawMessage) (any, error) {
		a.Runner.NewSession()
		return nil, nil
	}
	m["sessions.open"] = func(params json.RawMessage) (any, error) {
		var p pID
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		a.Runner.OpenSession(p.ID)
		return nil, nil
	}

	// permissions
	m["perm.respond"] = func(params json.RawMessage) (any, error) {
		var p pIDAction
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		a.Runner.RespondPerm(p.ID, p.Action)
		return nil, nil
	}

	// mode
	m["mode.get"] = func(json.RawMessage) (any, error) { return a.Runner.Mode(), nil }
	m["mode.set"] = func(params json.RawMessage) (any, error) {
		var p pMode
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		a.Runner.SetMode(p.Mode)
		return map[string]any{"mode": a.Runner.Mode()}, nil
	}

	// provider + index
	m["provider.state"] = func(json.RawMessage) (any, error) { return a.Monitor.Current(), nil }
	m["provider.probe"] = func(json.RawMessage) (any, error) {
		go a.Monitor.ProbeNow()
		return a.Monitor.Current(), nil
	}
	m["tie.state"] = func(json.RawMessage) (any, error) { return a.Tie.State(), nil }
	m["index.rebuild"] = func(json.RawMessage) (any, error) {
		go func() {
			n, err := a.Runner.RebuildIndex(context.Background())
			msg := map[string]any{"type": "tie.rebuilt", "sessions": n, "error": ""}
			if err != nil {
				msg["error"] = err.Error()
			}
			a.Bus.Emit(msg)
		}()
		return nil, nil
	}

	// config
	m["config.get"] = func(json.RawMessage) (any, error) {
		b, err := json.Marshal(a.Cfg)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(b), nil
	}
	m["config.set"] = func(params json.RawMessage) (any, error) {
		var p pDoc
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if err := a.SaveConfig(p.Doc); err != nil {
			return nil, &wrapped{codeHandler, err}
		}
		return map[string]any{"ok": true}, nil
	}

	// learning loop
	m["learn.now"] = func(json.RawMessage) (any, error) {
		a.Review.LearnNow()
		return nil, nil
	}
	m["memory.view"] = func(json.RawMessage) (any, error) {
		mem, _ := a.Mem.Read("memory")
		usr, _ := a.Mem.Read("user")
		return map[string]any{
			"memory": mem, "user": usr,
			"memory_cap": a.Cfg.Memory.MemoryCharLimit,
			"user_cap":   a.Cfg.Memory.UserCharLimit,
		}, nil
	}
	m["memory.pending"] = func(json.RawMessage) (any, error) {
		list, err := a.Mem.ListPending()
		if err != nil || list == nil {
			return []any{}, err
		}
		return list, nil
	}
	m["memory.respond"] = func(params json.RawMessage) (any, error) {
		var p pIDAction
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		a.RespondMemory(p.ID, p.Action)
		return nil, nil
	}
	m["skills.list"] = func(json.RawMessage) (any, error) {
		if l := a.Skills.List(); l != nil {
			return l, nil
		}
		return []any{}, nil
	}
	m["skills.pending"] = func(json.RawMessage) (any, error) {
		list, err := a.Skills.ListPending()
		if err != nil || list == nil {
			return []any{}, err
		}
		return list, nil
	}
	m["skill.respond"] = func(params json.RawMessage) (any, error) {
		var p pIDAction
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		a.RespondSkill(p.ID, p.Action)
		return nil, nil
	}
	m["journey.tail"] = func(params json.RawMessage) (any, error) {
		var p pN
		limit := 50
		if len(params) > 0 && json.Unmarshal(params, &p) == nil && p.N > 0 {
			limit = p.N
		}
		if l := a.Journey.Tail(limit); l != nil {
			return l, nil
		}
		return []any{}, nil
	}
	m["flags.peek"] = func(params json.RawMessage) (any, error) {
		var p pN
		n := 5
		if len(params) > 0 && json.Unmarshal(params, &p) == nil && p.N > 0 {
			n = p.N
		}
		if l := a.Review.PendingFlags(n); l != nil {
			return l, nil
		}
		return []any{}, nil
	}

	// harness + improvement
	m["harness.info"] = func(json.RawMessage) (any, error) { return a.Runner.HarnessInfo(), nil }
	m["harness.files"] = func(json.RawMessage) (any, error) {
		if l := a.Harness.Files(); l != nil {
			return l, nil
		}
		return []any{}, nil
	}
	m["harness.get"] = func(params json.RawMessage) (any, error) {
		var p pID
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		b, err := a.Harness.GetFile(p.ID)
		return map[string]any{"name": p.ID, "content": string(b), "error": errStr(err)}, nil
	}
	m["harness.save"] = func(params json.RawMessage) (any, error) {
		var p pNameContent
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		err := a.Harness.SaveFile(p.Name, p.Content)
		warns := a.Harness.Validate(a.Cfg.Harness.CharLimit)
		if warns == nil {
			warns = []string{}
		}
		a.Bus.Emit(map[string]any{"type": "harness.warn", "warns": warns, "saved": p.Name})
		return map[string]any{"error": errStr(err), "warns": warns}, nil
	}
	m["harness.lib"] = func(json.RawMessage) (any, error) {
		if l := a.Libs.List(); l != nil {
			return l, nil
		}
		return []any{}, nil
	}
	m["harness.lib_experience"] = func(params json.RawMessage) (any, error) {
		var p pID
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		b, err := a.Libs.Experience(p.ID)
		return map[string]any{"id": p.ID, "content": string(b), "error": errStr(err)}, nil
	}
	m["harness.lib_promote"] = func(params json.RawMessage) (any, error) {
		var p pID
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		backup, err := a.Libs.Promote(p.ID)
		a.Bus.Emit(map[string]any{"type": "harness.promoted", "entry": p.ID,
			"backup": backup, "error": errStr(err)})
		return map[string]any{"entry": p.ID, "backup": backup, "error": errStr(err)}, nil
	}
	m["improve.start"] = func(params json.RawMessage) (any, error) {
		var p pImprove
		if len(params) > 0 {
			_ = json.Unmarshal(params, &p)
		}
		if a.ImproveBusy() {
			return nil, &wrapped{codeHandler, fmt.Errorf("improve: already running")}
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
			defer cancel()
			if _, err := a.ImproveWith(ctx, p.K, p.Mode, p.Resume); err != nil {
				a.Bus.Emit(map[string]any{"type": "improve.error", "error": err.Error()})
			}
		}()
		return map[string]any{"started": true}, nil
	}

	return m
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
