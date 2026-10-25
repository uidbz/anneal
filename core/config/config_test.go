package config

import (
	"os"
	"testing"
)

func TestLoadCreatesDefaultsAndHonorsEnv(t *testing.T) {
	t.Setenv("FYAGENT_HOME", t.TempDir())
	t.Setenv("FYAGENT_ENDPOINT", "http://example.invalid/v1/chat/completions")
	t.Setenv("FYAGENT_MODEL", "m-env")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider.Endpoint != "http://example.invalid/v1/chat/completions" || cfg.Provider.Model != "m-env" {
		t.Fatalf("env overrides not applied: %+v", cfg.Provider)
	}
	if _, err := os.Stat(cfg.path); err != nil {
		t.Fatalf("default config not written: %v", err)
	}
	// Loading again reads the persisted document.
	cfg2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Agent.ToolIterationLimit != cfg.Agent.ToolIterationLimit {
		t.Fatalf("roundtrip changed agent config: %+v", cfg2.Agent)
	}
}

// Regression: Apply used to unmarshal into a zero Config, so a partial
// document silently wiped allow rules / compaction / tie settings.
func TestApplyKeepsDefaultsForOmittedKeys(t *testing.T) {
	t.Setenv("FYAGENT_HOME", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply([]byte(`{"provider":{"endpoint":"http://127.0.0.1:9999/v1/chat/completions"}}`)); err != nil {
		t.Fatal(err)
	}
	if cfg.Provider.Endpoint != "http://127.0.0.1:9999/v1/chat/completions" {
		t.Fatalf("endpoint not applied: %q", cfg.Provider.Endpoint)
	}
	d := Default()
	if len(cfg.Agent.Allow) != len(d.Agent.Allow) {
		t.Fatalf("allow rules reset by partial doc: %v", cfg.Agent.Allow)
	}
	if !cfg.Compaction.Enabled || !cfg.Tie.Enabled || !cfg.Harness.Enabled {
		t.Fatalf("feature toggles reset by partial doc: %+v", cfg)
	}
	if cfg.Provider.MaxTokens != d.Provider.MaxTokens {
		t.Fatalf("max_tokens: %d", cfg.Provider.MaxTokens)
	}
	// Persisted, and the path is preserved across the replacement.
	onDisk, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Provider.Endpoint != cfg.Provider.Endpoint {
		t.Fatalf("not persisted: %q", onDisk.Provider.Endpoint)
	}
}

func TestNormalizeFixesInvalidValues(t *testing.T) {
	c := Default()
	c.Provider.MaxTokens = -1
	c.Provider.MaxRetries = -2
	c.Agent.ToolIterationLimit = 0
	c.Agent.PermissionMode = "bogus"
	c.Compaction.ThresholdPct = 99
	c.Memory.MemoryCharLimit = 0
	c.Review.MaxAgeS = 0
	c.normalize()
	d := Default()
	if c.Provider.MaxTokens != d.Provider.MaxTokens || c.Provider.MaxRetries != d.Provider.MaxRetries {
		t.Fatalf("provider not normalized: %+v", c.Provider)
	}
	if c.Agent.ToolIterationLimit != d.Agent.ToolIterationLimit || c.Agent.PermissionMode != d.Agent.PermissionMode {
		t.Fatalf("agent not normalized: %+v", c.Agent)
	}
	if c.Compaction.ThresholdPct != d.Compaction.ThresholdPct || c.Memory.MemoryCharLimit != d.Memory.MemoryCharLimit ||
		c.Review.MaxAgeS != d.Review.MaxAgeS {
		t.Fatalf("sections not normalized: %+v", c)
	}
}
