// Package gateway bridges anneal's WebSocket protocol to messaging transports.
// It is a thin client: scheduling and agent execution stay in anneald. The
// packaged transports are ntfy and a plain-text Matrix room; the interface is
// designed for Telegram too.
package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/uidbz/anneal/core/fsutil"
	"github.com/uidbz/anneal/core/paths"
)

// Config is the gateway configuration document.
type Config struct {
	// Servers maps a short name to one anneald instance.
	Servers map[string]ServerConfig `json:"servers"`
	// Transports is the list of messaging backends. Multiple instances of the
	// same type are allowed (for example ntfy-home and ntfy-phone).
	Transports []TransportConfig `json:"transports"`
	// DefaultServer is used by chats that have not run "/use <name>".
	DefaultServer string `json:"default_server,omitempty"`
	// NotifyForeground sends a notification for foreground runs that did not
	// originate from a chat. Off by default to avoid mirroring the desktop.
	NotifyForeground bool `json:"notify_foreground,omitempty"`
	// StatePath stores per-chat server selection and delivery cursors.
	StatePath string `json:"state_path,omitempty"`
}

// ServerConfig names one anneald endpoint.
type ServerConfig struct {
	Addr     string `json:"addr"`                // unix:/path, host:port, ws://..., wss://...
	TokenEnv string `json:"token_env,omitempty"` // env var holding the hello token
}

// TransportConfig configures one messaging backend.
type TransportConfig struct {
	Name     string `json:"name"` // unique; used in job deliver targets
	Type     string `json:"type"` // "ntfy" (matrix/telegram reserved)
	BaseURL  string `json:"base_url,omitempty"`
	Topic    string `json:"topic,omitempty"`    // outbound notifications/replies
	InTopic  string `json:"in_topic,omitempty"` // inbound commands ("" = topic)
	TokenEnv string `json:"token_env,omitempty"`
	Title    string `json:"title,omitempty"`
	Priority int    `json:"priority,omitempty"`
	// Matrix.
	Homeserver string   `json:"homeserver,omitempty"`
	UserID     string   `json:"user_id,omitempty"`     // bot MXID, ignored as a sender
	RoomID     string   `json:"room_id,omitempty"`     // one room per transport
	AllowUsers []string `json:"allow_users,omitempty"` // human MXIDs allowed to command
	// HTTPListen enables the optional approval-action endpoint, e.g.
	// "127.0.0.1:8787". TLS should be terminated by a reverse proxy.
	HTTPListen string `json:"http_listen,omitempty"`
	// PublicURL is the externally reachable base URL used in ntfy action
	// buttons (must match HTTPListen through the proxy).
	PublicURL string `json:"public_url,omitempty"`
	// ActionTokenEnv authenticates action-button HTTP calls. Required when
	// PublicURL is set.
	ActionTokenEnv string `json:"action_token_env,omitempty"`
}

// LoadConfig reads path, creating a commented template on first run.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		path = paths.GatewayConfig()
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg := DefaultConfig()
		if err := SaveConfig(path, cfg); err != nil {
			return nil, err
		}
		return cfg, fmt.Errorf("created %s — edit it, then start the gateway again", path)
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SaveConfig writes the document atomically.
func SaveConfig(path string, cfg *Config) error {
	if path == "" {
		path = paths.GatewayConfig()
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(data, '\n'), 0o600)
}

func (c *Config) normalize() error {
	if c.StatePath == "" {
		c.StatePath = filepath.Join(paths.Root(), "gateway-state.json")
	}
	if len(c.Servers) == 0 {
		return fmt.Errorf("gateway: at least one server is required")
	}
	if c.DefaultServer == "" {
		names := make([]string, 0, len(c.Servers))
		for name := range c.Servers {
			names = append(names, name)
		}
		sort.Strings(names)
		c.DefaultServer = names[0]
	}
	if _, ok := c.Servers[c.DefaultServer]; !ok {
		return fmt.Errorf("gateway: default_server %q is not defined", c.DefaultServer)
	}
	seen := map[string]bool{}
	for i := range c.Transports {
		t := &c.Transports[i]
		t.Type = strings.ToLower(strings.TrimSpace(t.Type))
		if t.Name == "" {
			t.Name = t.Type
		}
		if seen[t.Name] {
			return fmt.Errorf("gateway: duplicate transport name %q", t.Name)
		}
		seen[t.Name] = true
		switch t.Type {
		case "ntfy":
			if t.BaseURL == "" || t.Topic == "" {
				return fmt.Errorf("gateway: ntfy %q needs base_url and topic", t.Name)
			}
			t.BaseURL = strings.TrimRight(t.BaseURL, "/")
			if t.InTopic == "" {
				t.InTopic = t.Topic
			}
			if t.Title == "" {
				t.Title = "anneal"
			}
		case "matrix":
			if t.Homeserver == "" || t.RoomID == "" || t.TokenEnv == "" {
				return fmt.Errorf("gateway: matrix %q needs homeserver, room_id and token_env", t.Name)
			}
			if t.UserID == "" {
				return fmt.Errorf("gateway: matrix %q needs user_id (the bot MXID)", t.Name)
			}
			if len(t.AllowUsers) == 0 {
				return fmt.Errorf("gateway: matrix %q needs allow_users (human MXIDs); refusing an open command room", t.Name)
			}
			t.Homeserver = strings.TrimRight(t.Homeserver, "/")
			if t.Title == "" {
				t.Title = "anneal"
			}
		default:
			return fmt.Errorf("gateway: unknown transport type %q", t.Type)
		}
	}
	if len(c.Transports) == 0 {
		return fmt.Errorf("gateway: at least one transport is required")
	}
	return nil
}

// DefaultConfig is a working local template.
func DefaultConfig() *Config {
	return &Config{
		Servers: map[string]ServerConfig{
			"local": {Addr: "unix:/run/anneal.sock", TokenEnv: "ANNEAL_AUTH_TOKEN"},
		},
		Transports: []TransportConfig{{
			Name:     "ntfy",
			Type:     "ntfy",
			BaseURL:  "http://127.0.0.1:2586",
			Topic:    "anneal",
			InTopic:  "anneal-in",
			TokenEnv: "NTFY_TOKEN",
			Title:    "anneal",
		}},
		DefaultServer: "local",
	}
}
