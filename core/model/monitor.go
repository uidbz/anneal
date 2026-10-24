package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"fyagent/core/config"
)

// Health states for the model endpoint.
const (
	StateConnecting   = "connecting"
	StateOnline       = "online"
	StateUnreachable  = "unreachable"
	StateWrongService = "wrong_service"
)

type Health struct {
	State     string   `json:"state"`
	Endpoint  string   `json:"endpoint"`
	Model     string   `json:"model"` // resolved model id
	Models    []string `json:"models"`
	CtxWindow int      `json:"ctx_window"` // provider-reported context window
	Error     string   `json:"error"`
	CheckedAt string   `json:"checked_at"`
}

// IsUsable reports whether runs can be submitted.
func (h Health) IsUsable() bool { return h.State == StateOnline }

// Monitor probes the configured endpoint's /v1/models and publishes Health
// snapshots. It also answers context-window questions for compaction.
type Monitor struct {
	providerMu sync.Mutex
	provider   config.Provider

	mu     sync.Mutex
	health Health
	on     func(Health)

	client *http.Client
}

func NewMonitor(p config.Provider, on func(Health)) *Monitor {
	m := &Monitor{
		provider: p,
		on:       on,
		client:   &http.Client{Timeout: 5 * time.Second},
		health:   Health{State: StateConnecting, Endpoint: p.Endpoint},
	}
	return m
}

// SetProvider re-points the monitor (settings changes) and probes at once.
func (m *Monitor) SetProvider(p config.Provider) {
	m.providerMu.Lock()
	m.provider = p
	m.providerMu.Unlock()
	go m.ProbeNow()
}

func (m *Monitor) Provider() config.Provider {
	m.providerMu.Lock()
	defer m.providerMu.Unlock()
	return m.provider
}

func (m *Monitor) Current() Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.health
}

// Start probes immediately, then on every interval until ctx ends.
func (m *Monitor) Start(ctx context.Context, interval time.Duration) {
	go func() {
		m.ProbeNow()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.ProbeNow()
			}
		}
	}()
}

// ProbeNow performs one health probe and publishes the result.
func (m *Monitor) ProbeNow() {
	p := m.Provider()
	h := Health{State: StateUnreachable, Endpoint: p.Endpoint, CheckedAt: time.Now().UTC().Format(time.RFC3339)}

	modelsURL, err := modelsURL(p.Endpoint)
	if err != nil {
		h.State = StateWrongService
		h.Error = err.Error()
	} else {
		resp, err := m.client.Get(modelsURL)
		switch {
		case err != nil:
			var uerr *url.Error
			if errors.As(err, &uerr) {
				h.Error = uerr.Err.Error()
			} else {
				h.Error = err.Error()
			}
		case resp.StatusCode != http.StatusOK:
			h.State = StateWrongService
			h.Error = fmt.Sprintf("GET %s → HTTP %d (is this a model server?)", modelsURL, resp.StatusCode)
		default:
			var doc struct {
				Data []struct {
					ID   string `json:"id"`
					Meta struct {
						Nctx int `json:"n_ctx"`
					} `json:"meta"`
				} `json:"data"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
				h.State = StateWrongService
				h.Error = "response is not a /v1/models document"
			} else if len(doc.Data) == 0 {
				h.Error = "model server reports no models"
			} else {
				h.State = StateOnline
				for _, md := range doc.Data {
					h.Models = append(h.Models, md.ID)
					if md.Meta.Nctx > h.CtxWindow {
						h.CtxWindow = md.Meta.Nctx
					}
				}
				h.Model = resolveModel(p.Model, h.Models)
				if p.ContextWindow > 0 {
					h.CtxWindow = p.ContextWindow
				}
			}
		}
		resp.Body.Close()
	}

	m.mu.Lock()
	m.health = h
	on := m.on
	m.mu.Unlock()
	if on != nil {
		on(h)
	}
}

// resolveModel maps the configured model ("auto" or explicit) onto the
// server-reported list.
func resolveModel(configured string, models []string) string {
	if configured != "" && configured != "auto" {
		for _, id := range models {
			if id == configured {
				return id
			}
		}
		return configured // server may still accept it
	}
	if len(models) > 0 {
		return models[0]
	}
	return ""
}

func modelsURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("bad endpoint %q: %w", endpoint, err)
	}
	path := strings.TrimSuffix(u.Path, "/chat/completions")
	if path == u.Path {
		path = strings.TrimSuffix(u.Path, "/")
	}
	u.Path = strings.TrimSuffix(path, "/") + "/models"
	return u.String(), nil
}
