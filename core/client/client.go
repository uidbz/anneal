// Package client speaks the anneal wire protocol (docs/PROTOCOL.md) on the
// caller's behalf: it owns the WebSocket, correlates JSON-RPC replies, and
// hands the raw bus events to consumers on a channel — the same maps the GUI
// and headless sinks render. Reconnect is the caller's job (re-Dial; the
// server replays session/mode/todo state after the hello ack).
//
// Addresses: "ws://host:port[/path]", or "unix:///path" (Unix socket, the
// default transport inside containers).
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ErrClosed is returned by Call after the connection died.
var ErrClosed = errors.New("anneal server connection closed")

// Event is one bus event exactly as the server emitted it.
type Event map[string]any

// Type returns the event's type key ("" when absent/not a string).
func (e Event) Type() string {
	if s, ok := e["type"].(string); ok {
		return s
	}
	return ""
}

// RPCError is a JSON-RPC error reply (server codes: -32000 handler,
// -32001 shutting down, spec codes for framing/params).
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc %d: %s", e.Code, e.Message) }

// Client is one protocol connection. One goroutine reads; Call is safe from
// any number of goroutines.
type Client struct {
	conn *websocket.Conn
	wmu  sync.Mutex // serializes frame writes

	mu      sync.Mutex
	pending map[string]chan reply
	nextID  int

	closeOnce sync.Once
	closeCh   chan struct{}

	// events carries bus events to the consumer; a full buffer drops the
	// NEWEST frame (ordered streams must not tear) and counts it.
	events  chan Event
	dropped atomic.Int64

	proto     int
	serverVer string
}

type reply struct {
	result json.RawMessage
	err    *RPCError
}

// Dial connects and performs the hello handshake. token must match the
// server's auth_token_env value when server auth is enabled.
func Dial(ctx context.Context, addr, token string) (*Client, error) {
	wsURL, dialer := endpoint(addr)
	conn, resp, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("dial %s: unexpected status %s", addr, resp.Status)
	}

	c := &Client{conn: conn, pending: map[string]chan reply{},
		closeCh: make(chan struct{}), events: make(chan Event, 1024)}

	hello := map[string]any{"type": "hello", "proto": 1, "client": "anneal-client"}
	if token != "" {
		hello["token"] = token
	}
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(hello); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var ack map[string]any
	if err := conn.ReadJSON(&ack); err != nil {
		conn.Close()
		return nil, err
	}
	if ack["type"] != "hello.ack" {
		conn.Close()
		if msg, ok := ack["error"].(string); ok && ack["type"] == "hello.reject" {
			return nil, errors.New("handshake rejected: " + msg)
		}
		return nil, errors.New("handshake failed: unexpected first frame")
	}
	if p, ok := ack["proto"].(float64); ok {
		c.proto = int(p)
	}
	if c.proto != ProtoVersion {
		conn.Close()
		return nil, fmt.Errorf("server protocol %d unsupported (client %d)", c.proto, ProtoVersion)
	}
	c.serverVer, _ = ack["server"].(string)
	_ = conn.SetReadDeadline(time.Time{})

	go c.readLoop()
	return c, nil
}

// endpoint maps the address forms to a dial target.
func endpoint(addr string) (string, *websocket.Dialer) {
	d := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	if rest, ok := strings.CutPrefix(addr, "unix:"); ok {
		path := strings.TrimPrefix(rest, "//")
		d.NetDialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var nd net.Dialer
			return nd.DialContext(ctx, "unix", path)
		}
		return "ws://anneal/ws", d
	}
	if !strings.Contains(addr, "://") {
		addr = "ws://" + addr
	}
	u, err := url.Parse(addr)
	if err == nil && (u.Path == "" || u.Path == "/") {
		u.Path = "/ws"
		addr = u.String()
	}
	return addr, d
}

// Call sends one JSON-RPC request and waits for its reply (or ctx death).
// params may be nil; the raw result JSON is returned for the caller to
// decode.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed() {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	c.nextID++
	id := strconv.Itoa(c.nextID)
	ch := make(chan reply, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	frame := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		frame["params"] = params
	}
	data, err := json.Marshal(frame)
	if err != nil {
		c.forget(id)
		return nil, err
	}
	c.wmu.Lock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	werr := c.conn.WriteMessage(websocket.TextMessage, data)
	c.wmu.Unlock()
	if werr != nil {
		c.forget(id)
		return nil, ErrClosed
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return r.result, nil
	case <-c.closeCh:
		c.forget(id)
		return nil, ErrClosed
	case <-ctx.Done():
		c.forget(id)
		return nil, ctx.Err()
	}
}

// CallInto decodes the result into out.
func (c *Client) CallInto(ctx context.Context, method string, params any, out any) error {
	raw, err := c.Call(ctx, method, params)
	if err != nil {
		return err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Events is the ordered bus-event stream (session/run/perm/... frames).
func (c *Client) Events() <-chan Event { return c.events }

// Dead closes when the connection is gone (read error or Close).
func (c *Client) Dead() <-chan struct{} { return c.closeCh }

// ServerVersion is the version string from the hello ack.
func (c *Client) ServerVersion() string { return c.serverVer }

// Proto is the negotiated protocol revision.
func (c *Client) Proto() int { return c.proto }

// ProtoVersion is the protocol revision this client speaks.
const ProtoVersion = 1

// Dropped counts events discarded because the consumer fell behind.
func (c *Client) Dropped() int64 { return c.dropped.Load() }

// Close tears the connection down; pending calls get ErrClosed. Events()
// closes too, so consumer range loops terminate.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.mu.Lock()
		close(c.closeCh)
		for id, ch := range c.pending {
			ch <- reply{err: &RPCError{Code: -1, Message: "connection closed"}}
			delete(c.pending, id)
		}
		close(c.events) // guarded by mu together with deliver
		c.mu.Unlock()
		_ = c.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(time.Second))
		err = c.conn.Close()
	})
	return err
}

func (c *Client) closed() bool {
	select {
	case <-c.closeCh:
		return true
	default:
		return false
	}
}

func (c *Client) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// readLoop decodes every frame once: JSON-RPC replies route to waiters,
// everything with a "type" is delivered as an event.
func (c *Client) readLoop() {
	defer c.Close()
	for {
		var m map[string]any
		if err := c.conn.ReadJSON(&m); err != nil {
			return
		}
		if v, ok := m["jsonrpc"]; ok && v == "2.0" {
			id, _ := m["id"].(string)
			c.mu.Lock()
			ch := c.pending[id]
			if ch != nil {
				delete(c.pending, id)
			}
			c.mu.Unlock()
			if ch == nil {
				continue
			}
			r := reply{}
			if e, ok := m["error"].(map[string]any); ok {
				code, _ := e["code"].(float64)
				msg, _ := e["message"].(string)
				r.err = &RPCError{Code: int(code), Message: msg}
			} else if res, ok := m["result"]; ok {
				r.result, _ = json.Marshal(res)
			}
			ch <- r
			continue
		}
		if _, ok := m["type"]; ok {
			c.deliver(Event(m))
		}
	}
}

// deliver is non-blocking; a stalled consumer sheds the newest frame rather
// than reordering the stream (the caller can resync with sessions.open).
// The mu guard makes close(events) in Close race-free: closeCh is closed
// under the same lock *before* events, so the guard rules out
// send-on-closed.
func (c *Client) deliver(e Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.closeCh:
		return
	default:
	}
	select {
	case c.events <- e:
	default:
		c.dropped.Add(1)
	}
}
