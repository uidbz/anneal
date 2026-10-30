package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestEventsChannelClosesOnClose verifies consumer range loops terminate
// when the connection dies (regression: leaked pump goroutines).
func TestEventsChannelClosesOnClose(t *testing.T) {
	c := stubDial(t)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	done := make(chan struct{})
	go func() {
		for range c.Events() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Events() never closed after Close")
	}
	if _, err := c.Call(context.Background(), "server.ping", nil); err != ErrClosed {
		t.Fatalf("Call after Close: %v", err)
	}
}

// stubDial connects to a minimal hello-compliant stub server.
func stubDial(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Upgrader{}.Upgrade(w, r)
		if err != nil {
			return
		}
		var hello map[string]any
		_ = ws.ReadJSON(&hello)
		_ = ws.WriteJSON(map[string]any{"type": "hello.ack", "proto": ProtoVersion, "server": "stub"})
		time.Sleep(2 * time.Second)
		ws.Close()
	}))
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	c, err := Dial(context.Background(), wsURL, "")
	if err != nil {
		t.Fatalf("dial stub: %v", err)
	}
	return c
}

func TestProtoMismatchRejected(t *testing.T) {
	// Spin a stub WS that acks with proto 2; Dial must refuse it
	// (PROTOCOL.md versioning rules).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Upgrader{}.Upgrade(w, r)
		if err != nil {
			return
		}
		var hello map[string]any
		_ = ws.ReadJSON(&hello)
		_ = ws.WriteJSON(map[string]any{"type": "hello.ack", "proto": 2, "server": "stub"})
		time.Sleep(time.Second)
		ws.Close()
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	if _, err := Dial(context.Background(), wsURL, ""); err == nil {
		t.Fatal("proto 2 server should be rejected")
	}
}
