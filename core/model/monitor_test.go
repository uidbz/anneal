package model

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uidbz/anneal/core/config"
)

func testProvider(endpoint string) config.Provider {
	return config.Provider{Endpoint: endpoint, Model: "auto", ContextWindow: 0}
}

func TestProbeNowOnlineResolvesModelAndWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("probed %q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"small","meta":{"n_ctx":8192}},
			{"id":"big","meta":{"n_ctx":65536}}
		]}`))
	}))
	defer srv.Close()

	var got Health
	m := NewMonitor(testProvider(srv.URL+"/v1/chat/completions"), func(h Health) { got = h })
	m.ProbeNow()

	if got.State != StateOnline || got.Model != "small" || got.CtxWindow != 65536 {
		t.Fatalf("health: %+v", got)
	}
	if len(got.Models) != 2 || got.Endpoint == "" || got.CheckedAt == "" {
		t.Fatalf("health fields: %+v", got)
	}
}

func TestProbeNowExplicitWindowOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m","meta":{"n_ctx":4096}}]}`))
	}))
	defer srv.Close()

	p := testProvider(srv.URL + "/v1/chat/completions")
	p.Model = "m"
	p.ContextWindow = 12345
	m := NewMonitor(p, nil)
	m.ProbeNow()
	if h := m.Current(); h.Model != "m" || h.CtxWindow != 12345 {
		t.Fatalf("health: %+v", h)
	}
}

func TestProbeNowWrongService(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m := NewMonitor(testProvider(srv.URL+"/v1/chat/completions"), nil)
	m.ProbeNow()
	h := m.Current()
	if h.State != StateWrongService || !strings.Contains(h.Error, "HTTP 404") {
		t.Fatalf("health: %+v", h)
	}
}

// Regression: net/http returns a nil response for transport errors. The probe
// used to call resp.Body.Close() unconditionally and panicked whenever the
// model server was down.
func TestProbeNowUnreachableDoesNotPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := srv.URL + "/v1/chat/completions"
	srv.Close()

	m := NewMonitor(testProvider(endpoint), nil)
	m.ProbeNow() // must not panic
	h := m.Current()
	if h.State != StateUnreachable {
		t.Fatalf("state = %q (error %q), want unreachable", h.State, h.Error)
	}
	if h.Error == "" {
		t.Fatal("transport error not reported")
	}
}

func TestSettingsCarriesReasoningEffort(t *testing.T) {
	cfg := config.Default()
	cfg.Provider.ReasoningEffort = "high"
	ms := Settings(cfg)
	if ms == nil || ms.ReasoningEffort != "high" {
		t.Fatalf("settings: %+v", ms)
	}
	cfg.Provider.ReasoningEffort = "  "
	if ms := Settings(cfg); ms.ReasoningEffort != "" {
		t.Fatalf("blank effort should be omitted: %+v", ms)
	}
}
