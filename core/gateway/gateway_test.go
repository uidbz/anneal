package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLoadConfigValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.json")
	good := `{
	  "servers": {"home": {"addr": "unix:/tmp/anneal.sock"}},
	  "default_server": "home",
	  "transports": [{"name":"ntfy","type":"ntfy","base_url":"http://127.0.0.1:2586","topic":"anneal","in_topic":"anneal-in"}],
	  "state_path": "` + filepath.Join(dir, "state.json") + `"
	}`
	if err := writeFile(path, good); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transports[0].InTopic != "anneal-in" || cfg.DefaultServer != "home" {
		t.Fatalf("normalized: %+v", cfg)
	}

	bad := `{"servers":{"a":{"addr":"x"}},"transports":[{"name":"x","type":"bogus"}]}`
	if err := writeFile(path, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("unknown transport accepted")
	}
}

func TestParseRefAndSilent(t *testing.T) {
	r, ok := parseRef(makeRef(route{transport: "ntfy", chat: "anneal-in"}))
	if !ok || r.transport != "ntfy" || r.chat != "anneal-in" {
		t.Fatalf("ref: %+v %v", r, ok)
	}
	if _, ok := parseRef("nope"); ok {
		t.Fatal("bad ref accepted")
	}
	for _, s := range []string{"[SILENT]", "silent", "NO_REPLY", " No Reply "} {
		if !silent(s) {
			t.Errorf("%q not silent", s)
		}
	}
	if silent("done") {
		t.Fatal("normal text suppressed")
	}
}

func TestTrimText(t *testing.T) {
	got := trimText("abcdef", 3)
	if !strings.HasPrefix(got, "abc") || !strings.Contains(got, "truncated") {
		t.Fatalf("trim: %q", got)
	}
}

func TestNtfySend(t *testing.T) {
	var (
		mu  sync.Mutex
		got struct {
			path, title, priority, tags, auth, body string
		}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got.path = r.URL.Path
		got.title = r.Header.Get("Title")
		got.priority = r.Header.Get("Priority")
		got.tags = r.Header.Get("Tags")
		got.auth = r.Header.Get("Authorization")
		got.body = string(b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1"}`))
	}))
	defer srv.Close()

	tr, err := newNtfyTransport(TransportConfig{
		Name: "ntfy", Type: "ntfy", BaseURL: srv.URL, Topic: "out", InTopic: "in", TokenEnv: "TEST_NTFY_TOKEN",
	})
	if err != nil {
		t.Fatal(err)
	}
	tr.token = "secret"
	if err := tr.Send(context.Background(), "out", Message{Title: "hello", Text: "body", Priority: 4, Tags: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.path != "/out" || got.title != "hello" || got.priority != "4" || got.tags != "x" || got.auth != "Bearer secret" || got.body != "body" {
		t.Fatalf("request: %+v", got)
	}
}

func TestNtfySubscribe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/in/json" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl := w.(http.Flusher)
		_, _ = w.Write([]byte(`{"id":"m1","event":"message","topic":"in","message":"hello","time":1700000000}` + "\n"))
		fl.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	tr, err := newNtfyTransport(TransportConfig{Name: "ntfy", Type: "ntfy", BaseURL: srv.URL, Topic: "out", InTopic: "in"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := openState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inbox := make(chan Inbound, 4)
	done := make(chan error, 1)
	go func() { done <- tr.Start(ctx, inbox, st) }()
	select {
	case in := <-inbox:
		if in.Text != "hello" || in.Chat != "out" {
			t.Fatalf("inbound: %+v", in)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no inbound message")
	}
	if got := st.Cursor("ntfy:" + srv.URL + ":in"); got != "m1" {
		t.Fatalf("cursor = %q", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("transport did not stop")
	}
}

type fakeTransport struct {
	mu   sync.Mutex
	sent []Message
	home string
}

func (f *fakeTransport) Name() string     { return "fake" }
func (f *fakeTransport) HomeChat() string { return f.home }
func (f *fakeTransport) Start(context.Context, chan<- Inbound, Cursor) error {
	return nil
}
func (f *fakeTransport) Send(ctx context.Context, chat string, msg Message) error {
	f.mu.Lock()
	f.sent = append(f.sent, msg)
	f.mu.Unlock()
	return nil
}
func (f *fakeTransport) Close() error { return nil }

func TestScheduleFinishedDelivery(t *testing.T) {
	ft := &fakeTransport{home: "home"}
	g := &Gateway{
		cfg:        Config{},
		transports: map[string]Transport{"fake": ft},
		out:        make(chan delivery, 10),
	}
	ev := map[string]any{
		"type": "schedule.finished",
		"job": map[string]any{
			"id": "daily", "name": "Daily", "deliver": []any{"fake"}, "next_run": "2026-11-23T07:00:00Z",
		},
		"record": map[string]any{
			"status": "ok", "text": "the summary", "duration_s": float64(12), "session_id": "s1",
		},
	}
	g.onScheduleFinished("home", ev)
	select {
	case d := <-g.out:
		if d.transport != "fake" || !strings.Contains(d.msg.Text, "the summary") {
			t.Fatalf("delivery: %+v", d)
		}
		if !strings.Contains(d.msg.Text, "next:") {
			t.Fatalf("missing next: %q", d.msg.Text)
		}
	default:
		t.Fatal("no delivery queued")
	}

	// Silent output is suppressed.
	ev["record"].(map[string]any)["text"] = "[SILENT]"
	g.onScheduleFinished("home", ev)
	select {
	case d := <-g.out:
		t.Fatalf("silent delivered: %+v", d)
	default:
	}
}

func TestMatrixSyncAndSend(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
		put   struct{ path, body, auth string }
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/sync"):
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				_, _ = w.Write([]byte(`{"next_batch":"s1","rooms":{"join":{"!room:test":{"timeline":{"events":[{"type":"m.room.message","event_id":"$old","sender":"@johan:test","origin_server_ts":1,"content":{"msgtype":"m.text","body":"old"}}]}}}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"next_batch":"s2","rooms":{"join":{"!room:test":{"timeline":{"events":[{"type":"m.room.message","event_id":"$new","sender":"@johan:test","origin_server_ts":2,"content":{"msgtype":"m.text","body":"hello"}},{"type":"m.room.message","event_id":"$bad","sender":"@stranger:test","origin_server_ts":3,"content":{"msgtype":"m.text","body":"nope"}}]}}}}}`))
		case r.Method == http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			put.path, put.body, put.auth = r.URL.Path, string(b), r.Header.Get("Authorization")
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"event_id":"$sent"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("TEST_MATRIX_TOKEN", "tok")
	tr, err := newMatrixTransport(TransportConfig{
		Name: "matrix", Type: "matrix", Homeserver: srv.URL, RoomID: "!room:test",
		UserID: "@anneal:test", AllowUsers: []string{"@johan:test"}, TokenEnv: "TEST_MATRIX_TOKEN",
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := openState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inbox := make(chan Inbound, 4)
	done := make(chan error, 1)
	go func() { done <- tr.Start(ctx, inbox, st) }()
	select {
	case in := <-inbox:
		if in.Text != "hello" || in.Chat != "!room:test" {
			t.Fatalf("inbound: %+v", in)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no matrix inbound")
	}
	cancel()
	<-done

	if err := tr.Send(context.Background(), "!room:test", Message{Title: "anneal", Text: "body"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(put.path, "/rooms/!room:test/send/m.room.message/") || !strings.Contains(put.body, "body") || put.auth != "Bearer tok" {
		t.Fatalf("send: %+v", put)
	}
}

func TestMatrixConfigRequiresAllowlist(t *testing.T) {
	cfg := Config{
		Servers: map[string]ServerConfig{"a": {Addr: "x"}},
		Transports: []TransportConfig{{
			Name: "matrix", Type: "matrix", Homeserver: "https://m", RoomID: "!r:m", UserID: "@a:m", TokenEnv: "T",
		}},
	}
	if err := cfg.normalize(); err == nil {
		t.Fatal("matrix without allow_users accepted")
	}
}

func TestSummarizeJob(t *testing.T) {
	job := map[string]any{"id": "x", "name": "Daily", "enabled": false, "next_run": "2026-01-01T00:00:00Z", "last_status": "ok"}
	got := summarizeJob(job)
	if !strings.Contains(got, "paused") || !strings.Contains(got, "Daily") || !strings.Contains(got, "last=ok") {
		t.Fatalf("summary: %q", got)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
