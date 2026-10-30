package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uidbz/anneal/core/app"
	"github.com/uidbz/anneal/core/client"

	"github.com/gorilla/websocket"
)

// startTest boots the full app against a temp ANNEAL_HOME and serves the
// protocol on a random loopback port. No LLM/tie connection is started.
func startTest(t *testing.T, mutate func(s *Server)) (*Server, *client.Client) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ANNEAL_HOME", home)
	t.Setenv("ANNEAL_LISTEN", "127.0.0.1:0")

	a, err := app.New()
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	a.Cfg.Server.Listen = []string{"127.0.0.1:0"}
	s, err := New(a, "test")
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	if mutate != nil {
		mutate(s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	addr := waitAddr(t, s)
	c, err := client.Dial(ctx, "ws://"+addr, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return s, c
}

func waitAddr(t *testing.T, s *Server) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if addrs := s.Addrs(); len(addrs) > 0 {
			return addrs[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server never started listening")
	return ""
}

func callOK(t *testing.T, c *client.Client, method string, params any) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := c.Call(ctx, method, params)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return raw
}

func TestPingAndInfo(t *testing.T) {
	_, c := startTest(t, nil)
	var res struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(callOK(t, c, "server.ping", nil), &res); err != nil || !res.OK {
		t.Fatalf("server.ping: %v (%+v)", err, res)
	}
	var info struct {
		Version string `json:"version"`
		Proto   int    `json:"proto"`
		Busy    bool   `json:"busy"`
	}
	callInto(t, c, "server.info", nil, &info)
	if info.Version != "test" || info.Proto != ProtoVersion {
		t.Fatalf("server.info: %+v", info)
	}
}

func callInto(t *testing.T, c *client.Client, method string, params any, out any) {
	t.Helper()
	if err := c.CallInto(context.Background(), method, params, out); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}

func TestUnknownMethod(t *testing.T) {
	_, c := startTest(t, nil)
	_, err := c.Call(context.Background(), "no.such", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var rpcErr *client.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != codeNoMethod {
		t.Fatalf("want %d, got %v", codeNoMethod, err)
	}
}

func TestAuthToken(t *testing.T) {
	t.Setenv("ANNEAL_AUTH_TOKEN", "s3cret")
	home := t.TempDir()
	t.Setenv("ANNEAL_HOME", home)
	a, err := app.New()
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	a.Cfg.Server.Listen = []string{"127.0.0.1:0"}
	s, err := New(a, "test")
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	if s.token != "s3cret" {
		t.Fatalf("token not resolved from env: %q", s.token)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	addr := waitAddr(t, s)

	if _, err := client.Dial(ctx, "ws://"+addr, ""); err == nil {
		t.Fatal("dial without token should be rejected")
	}
	c, err := client.Dial(ctx, "ws://"+addr, "s3cret")
	if err != nil {
		t.Fatalf("dial with token: %v", err)
	}
	defer c.Close()
	callOK(t, c, "server.ping", nil)
}

func TestModeRoundtrip(t *testing.T) {
	_, c := startTest(t, nil)
	var got struct {
		Mode string `json:"mode"`
	}
	callInto(t, c, "mode.set", map[string]any{"mode": "plan"}, &got)
	if got.Mode != "plan" {
		t.Fatalf("mode.set: %+v", got)
	}
	mode := string(callOK(t, c, "mode.get", nil))
	if mode != `"plan"` {
		t.Fatalf("mode.get: %s", mode)
	}
}

func TestEventFanout(t *testing.T) {
	s, c := startTest(t, nil)
	// Drain the connect-time replay (session.opened/mode/todo) first, then
	// expect the emitted event in order.
	s.App.Bus.Emit(map[string]any{"type": "test.echo", "n": 42.0})
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-c.Events():
			if e.Type() == "test.echo" {
				if e["n"] != 42.0 {
					t.Fatalf("event payload: %+v", e)
				}
				return
			}
		case <-deadline:
			t.Fatal("no event received")
		}
	}
}

func TestSessionsAndPermMethodsReachCore(t *testing.T) {
	_, c := startTest(t, nil)
	var sessions []map[string]any
	callInto(t, c, "sessions.list", nil, &sessions)
	// A fresh home resumes/creates one session; just require valid JSON here.
	callOK(t, c, "perm.respond", map[string]any{"id": "missing", "action": "deny"})
	callOK(t, c, "learn.now", nil)
}

func TestIdleShutdown(t *testing.T) {
	s, _ := startTest(t, func(s *Server) { s.cfg.IdleTimeoutS = 1 })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s.Reason() == "idle" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("idle shutdown did not fire (reason=%q)", s.Reason())
}

func TestIdleConnectionSurvivesHelloDeadline(t *testing.T) {
	// Regression: the hello read deadline must not persist into the RPC
	// loop, or every idle connection dies 10 s after connecting.
	s, c := startTest(t, func(s *Server) { s.cfg.IdleTimeoutS = 0 })
	_ = s
	// Wait past the old 10 s hello deadline, then call normally.
	time.Sleep(11 * time.Second)
	callOK(t, c, "server.ping", nil)
	select {
	case <-c.Dead():
		t.Fatal("idle connection was reaped after the hello deadline")
	default:
	}
}

// TestBrowserOriginRejectedWithoutToken: with auth disabled, a handshake
// carrying an Origin header (i.e. from a browser) must be refused.
func TestBrowserOriginRejectedWithoutToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ANNEAL_HOME", home)
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg.Server.Listen = []string{"127.0.0.1:0"}
	s, err := New(a, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	addr := waitAddr(t, s)

	d := websocket.Dialer{}
	hdr := http.Header{}
	hdr.Set("Origin", "http://evil.example")
	_, resp, err := d.Dial("ws://"+addr+"/ws", hdr)
	if err == nil {
		t.Fatal("expected rejection of browser origin without token")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %v", resp)
	}
	// Native (no Origin) still works.
	c2, err := client.Dial(ctx, "ws://"+addr, "")
	if err != nil {
		t.Fatalf("native dial should work: %v", err)
	}
	c2.Close()
}

func TestTokenlessBindPolicy(t *testing.T) {
	cases := map[string]bool{
		"unix:/tmp/x.sock": true,
		"127.0.0.1:7777":   true,
		"[::1]:7777":       true,
		"localhost:7777":   true,
		"0.0.0.0:7777":     false,
		":7777":            false,
		"10.0.0.5:7777":    false,
	}
	for addr, ok := range cases {
		if got := tokenlessBindOK(addr); got != ok {
			t.Errorf("tokenlessBindOK(%q) = %v, want %v", addr, got, ok)
		}
	}
	// And Run enforces it: wildcard bind without a token must fail.
	t.Setenv("ANNEAL_HOME", t.TempDir())
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg.Server.Listen = []string{"0.0.0.0:0"}
	s, err := New(a, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("wildcard tokenless bind should refuse to start")
	}
}

// TestNotificationNoReply: a request frame without an id must never be
// answered, even for unknown methods; a following normal request works.
func TestNotificationNoReply(t *testing.T) {
	s, _ := startTest(t, nil)
	addr := s.Addrs()[0]
	ws, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if err := ws.WriteJSON(map[string]any{"type": "hello", "proto": ProtoVersion}); err != nil {
		t.Fatal(err)
	}
	var ack map[string]any
	if err := ws.ReadJSON(&ack); err != nil || ack["type"] != "hello.ack" {
		t.Fatalf("ack: %v %v", ack, err)
	}
	// Notifications first, then a normal request; collect frames until the
	// ping reply. Any jsonrpc frame with a foreign id means the server
	// wrongly answered a notification. (Do not drain via read-timeout:
	// gorilla marks the conn permanently bad after an i/o timeout.)
	for _, note := range []any{
		map[string]any{"jsonrpc": "2.0", "method": "mode.get"},
		map[string]any{"jsonrpc": "2.0", "method": "no.such"},
	} {
		if err := ws.WriteJSON(note); err != nil {
			t.Fatal(err)
		}
	}
	if err := ws.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": "9", "method": "server.ping"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ws.SetReadDeadline(deadline)
		var f map[string]any
		if err := ws.ReadJSON(&f); err != nil {
			t.Fatalf("waiting for ping reply: %v", err)
		}
		if _, isReply := f["jsonrpc"]; !isReply {
			continue // replay/bus events
		}
		if f["id"] == "9" {
			return
		}
		t.Fatalf("server replied to a notification: %v", f)
	}
}

// TestUnixSocketRoundtrip: the container-default transport — listen on a
// unix socket in the mount dir, client dials unix://, mode 0600.
func TestUnixSocketRoundtrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ANNEAL_HOME", home)
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(home, "anneal.sock")
	a.Cfg.Server.Listen = []string{"unix:" + sock}
	s, err := New(a, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	waitAddr(t, s)

	c, err := client.Dial(ctx, "unix://"+sock, "")
	if err != nil {
		t.Fatalf("unix dial: %v", err)
	}
	defer c.Close()
	callOK(t, c, "server.ping", nil)
	if fi, err := os.Stat(sock); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %o, want 0600", fi.Mode().Perm())
	}
}

// TestSlowConsumerDisconnected: a client that stops reading the socket is
// closed once the server queue overflows; the survivor keeps streaming.
func TestSlowConsumerDisconnected(t *testing.T) {
	s, good := startTest(t, nil)
	addr := s.Addrs()[0]
	stub, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stub.Close()
	if err := stub.WriteJSON(map[string]any{"type": "hello", "proto": ProtoVersion}); err != nil {
		t.Fatal(err)
	}
	var ack map[string]any
	if err := stub.ReadJSON(&ack); err != nil {
		t.Fatal(err)
	}
	// Wait for registration: two conns live (startTest client + stub).
	regDeadline := time.Now().Add(3 * time.Second)
	for s.connsCount() < 2 && time.Now().Before(regDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.connsCount() < 2 {
		t.Fatal("stub client was never registered")
	}
	// Now never read again. Loopback socket buffers are large, so the
	// flood must be big enough (24 MB) to force the write path to stall
	// and the per-connection queue to overflow.
	payload := strings.Repeat("x", 16*1024)
	for i := 0; i < connQueue+512; i++ {
		s.App.Bus.Emit(map[string]any{"type": "flood", "p": payload})
	}
	deadline := time.Now().Add(15 * time.Second)
	for s.connsCount() > 1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if s.connsCount() > 1 {
		t.Fatalf("unreading client was not disconnected (conns=%d)", s.connsCount())
	}
	s.App.Bus.Emit(map[string]any{"type": "after.flood"})
	for {
		select {
		case e := <-good.Events():
			if e.Type() == "after.flood" {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("survivor lost its stream")
		}
	}
}

func TestQuitRPC(t *testing.T) {
	s, c := startTest(t, nil)
	callOK(t, c, "server.quit", map[string]any{"reason": "test"})
	if s.Reason() != "test" {
		t.Fatalf("reason=%q", s.Reason())
	}
	// After shutdown, non-quit methods are refused.
	if _, err := c.Call(context.Background(), "mode.get", nil); err == nil {
		t.Fatal("expected refusal after shutdown")
	}
}
