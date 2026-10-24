// Package config defines fyagent's configuration (~/.fyagent/config.json),
// with defaults, env overrides, and atomic saves.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"fyagent/core/paths"
)

type Provider struct {
	Endpoint                 string  `json:"endpoint"`
	Model                    string  `json:"model"` // "auto" = first id from /v1/models
	APIKey                   string  `json:"api_key"`
	ContextWindow            int     `json:"context_window"` // 0 = provider-reported
	MaxTokens                int     `json:"max_tokens"`
	Temperature              float64 `json:"temperature"`
	ReasoningEffort          string  `json:"reasoning_effort"` // "" | low | medium | high
	StreamFirstEventTimeoutS int     `json:"stream_first_event_timeout_s"`
	StreamIdleTimeoutS       int     `json:"stream_idle_timeout_s"`
	MaxRetries               int     `json:"max_retries"`
}

type Agent struct {
	PermissionMode     string   `json:"permission_mode"` // default|plan|acceptEdits|bypassPermissions|dontAsk
	Allow              []string `json:"allow"`
	Ask                []string `json:"ask"`
	Deny               []string `json:"deny"`
	ToolIterationLimit int      `json:"tool_iteration_limit"`
	Subagents          bool     `json:"subagents"`
}

type Compaction struct {
	Enabled         bool `json:"enabled"`
	ThresholdPct    int  `json:"threshold_pct"`
	KeepRecentTurns int  `json:"keep_recent_turns"`
}

type Memory struct {
	Enabled         bool `json:"enabled"`
	MemoryCharLimit int  `json:"memory_char_limit"`
	UserCharLimit   int  `json:"user_char_limit"`
	WriteApproval   bool `json:"write_approval"`
}

type Skills struct {
	Enabled       bool     `json:"enabled"`
	WriteApproval bool     `json:"write_approval"`
	ExtraDirs     []string `json:"extra_dirs"`
}

type Review struct {
	Enabled        bool `json:"enabled"`
	DeferUntilIdle bool `json:"defer_until_idle"`
	IdleSettleS    int  `json:"idle_settle_s"`
	MaxAgeS        int  `json:"max_age_s"`
	MaxInputTokens int  `json:"max_input_tokens"`
	Notify         bool `json:"notify"`
}

// Tie configures the tie-backed episodic index (session_search).
type Tie struct {
	Enabled    bool   `json:"enabled"`
	Collection string `json:"collection"`  // tie config collection name
	ConfigName string `json:"config_name"` // tie client config (client.LoadConfig); "" = default
}

// MCPServer mirrors dive's mcp.ServerConfig (stdio via Command or
// streamable HTTP via URL), Anthropic mcp.json-shaped.
type MCPServer struct {
	Type     string            `json:"type,omitempty"` // stdio|http (inferred if empty)
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	URL      string            `json:"url,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

type MCP struct {
	Enabled bool                 `json:"enabled"`
	Servers map[string]MCPServer `json:"servers"`
}

type UI struct {
	UIDir string `json:"ui_dir"` // overrides embedded UI scripts
}

type Config struct {
	Provider   Provider   `json:"provider"`
	Agent      Agent      `json:"agent"`
	Compaction Compaction `json:"compaction"`
	Memory     Memory     `json:"memory"`
	Skills     Skills     `json:"skills"`
	Review     Review     `json:"review"`
	Tie        Tie        `json:"tie"`
	MCP        MCP        `json:"mcp"`
	UI         UI         `json:"ui"`

	path string `json:"-"`
}

func Default() *Config {
	return &Config{
		Provider: Provider{
			Endpoint:                 "http://127.0.0.1:8080/v1/chat/completions",
			Model:                    "auto",
			APIKey:                   "strata",
			ContextWindow:            0,
			MaxTokens:                16384,
			Temperature:              0.4,
			ReasoningEffort:          "medium",
			StreamFirstEventTimeoutS: 240,
			StreamIdleTimeoutS:       120,
			MaxRetries:               2,
		},
		Agent: Agent{
			PermissionMode:     "default",
			Allow:              []string{"Read", "Glob", "Grep", "ListDirectory", "todo", "session_search", "memory", "skill_manage", "Skill"},
			Ask:                []string{"Bash", "Write", "Edit", "WebFetch"},
			Deny:               nil,
			ToolIterationLimit: 80,
			Subagents:          true,
		},
		Compaction: Compaction{Enabled: true, ThresholdPct: 82, KeepRecentTurns: 4},
		Memory:     Memory{Enabled: true, MemoryCharLimit: 2200, UserCharLimit: 1375, WriteApproval: false},
		Skills:     Skills{Enabled: true, WriteApproval: false, ExtraDirs: nil},
		Review:     Review{Enabled: true, DeferUntilIdle: true, IdleSettleS: 45, MaxAgeS: 1800, MaxInputTokens: 48000, Notify: true},
		Tie:        Tie{Enabled: true, Collection: "fyagent", ConfigName: ""},
		MCP:        MCP{Enabled: true, Servers: map[string]MCPServer{}},
		UI:         UI{},
	}
}

// Load reads the config file (writing defaults on first run) and applies
// env overrides FYAGENT_ENDPOINT / FYAGENT_MODEL.
func Load() (*Config, error) {
	cfg := Default()
	cfg.path = paths.Config()

	if err := paths.Ensure(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(cfg.path)
	switch {
	case os.IsNotExist(err):
		if err := cfg.save(); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
	}

	if v := os.Getenv("FYAGENT_ENDPOINT"); v != "" {
		cfg.Provider.Endpoint = v
	}
	if v := os.Getenv("FYAGENT_MODEL"); v != "" {
		cfg.Provider.Model = v
	}
	if v := os.Getenv("FYAGENT_UI_DIR"); v != "" {
		cfg.UI.UIDir = v
	}
	cfg.normalize()
	return cfg, nil
}

// Apply parses a full-config JSON document (from the settings UI) and
// replaces the current values, then persists.
func (c *Config) Apply(doc []byte) error {
	var next Config
	if err := json.Unmarshal(doc, &next); err != nil {
		return err
	}
	path := c.path
	*c = next
	c.path = path
	c.normalize()
	return c.save()
}

func (c *Config) Save() error { return c.save() }

func (c *Config) save() error {
	if c.path == "" {
		c.path = paths.Config()
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func (c *Config) normalize() {
	d := Default()
	if c.Provider.MaxTokens <= 0 {
		c.Provider.MaxTokens = d.Provider.MaxTokens
	}
	if c.Provider.StreamFirstEventTimeoutS <= 0 {
		c.Provider.StreamFirstEventTimeoutS = d.Provider.StreamFirstEventTimeoutS
	}
	if c.Provider.StreamIdleTimeoutS <= 0 {
		c.Provider.StreamIdleTimeoutS = d.Provider.StreamIdleTimeoutS
	}
	if c.Provider.MaxRetries < 0 {
		c.Provider.MaxRetries = d.Provider.MaxRetries
	}
	if c.Agent.ToolIterationLimit <= 0 {
		c.Agent.ToolIterationLimit = d.Agent.ToolIterationLimit
	}
	switch c.Agent.PermissionMode {
	case "default", "plan", "acceptEdits", "bypassPermissions", "dontAsk":
	default:
		c.Agent.PermissionMode = d.Agent.PermissionMode
	}
	if c.Compaction.ThresholdPct <= 0 || c.Compaction.ThresholdPct > 95 {
		c.Compaction.ThresholdPct = d.Compaction.ThresholdPct
	}
	if c.Memory.MemoryCharLimit <= 0 {
		c.Memory.MemoryCharLimit = d.Memory.MemoryCharLimit
	}
	if c.Memory.UserCharLimit <= 0 {
		c.Memory.UserCharLimit = d.Memory.UserCharLimit
	}
	if c.Review.IdleSettleS < 0 {
		c.Review.IdleSettleS = d.Review.IdleSettleS
	}
	if c.Review.MaxAgeS <= 0 {
		c.Review.MaxAgeS = d.Review.MaxAgeS
	}
}

// StreamTimeouts materializes the provider stream timeout durations.
func (p Provider) StreamTimeouts() (firstEvent, idle time.Duration) {
	return time.Duration(p.StreamFirstEventTimeoutS) * time.Second,
		time.Duration(p.StreamIdleTimeoutS) * time.Second
}
