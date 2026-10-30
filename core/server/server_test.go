package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/uidbz/anneal/core/app"
	"github.com/uidbz/anneal/core/client"
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
