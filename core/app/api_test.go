package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/uidbz/anneal/core/memory"
	"github.com/uidbz/anneal/core/skills"
)

func TestSaveConfigAndStageResolution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ANNEAL_HOME", home)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Review.Interrupt() })

	var events []string
	a.Bus.SetTap(func(v any) {
		if m, ok := v.(map[string]any); ok {
			if typ, ok := m["type"].(string); ok {
				events = append(events, typ)
			}
		}
	})

	// SaveConfig applies, persists, re-points the monitor, and announces.
	if err := a.SaveConfig(`{"provider":{"endpoint":"http://example.invalid/v1/chat/completions","model":"m","max_tokens":123}}`); err != nil {
		t.Fatal(err)
	}
	if a.Cfg.Provider.Endpoint != "http://example.invalid/v1/chat/completions" || a.Cfg.Provider.MaxTokens != 123 {
		t.Fatalf("config not applied: %+v", a.Cfg.Provider)
	}
	if a.Monitor.Provider().Endpoint != a.Cfg.Provider.Endpoint {
		t.Fatalf("monitor not re-pointed: %+v", a.Monitor.Provider())
	}
	if _, err := os.Stat(filepath.Join(home, "config.json")); err != nil {
		t.Fatalf("config not persisted: %v", err)
	}
	if !contains(events, "config.changed") {
		t.Fatalf("config.changed missing: %v", events)
	}

	// Memory resolution: approve applies and emits updated+resolved.
	mop, err := a.Mem.Stage(&memory.Op{Target: "user", Action: "add", Content: "prefers tabs", Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	events = nil
	a.RespondMemory(mop.ID, "approve")
	if cur, _ := a.Mem.Read("user"); cur != "- prefers tabs\n" {
		t.Fatalf("memory not applied: %q", cur)
	}
	if !contains(events, "memory.updated") || !contains(events, "memory.resolved") {
		t.Fatalf("memory events: %v", events)
	}

	// Skill resolution: reject leaves no skill and emits resolved.
	sop, err := a.Skills.Stage(&skills.Op{Action: "create", Name: "test-skill",
		Description: "d", Content: "body", Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	events = nil
	a.RespondSkill(sop.ID, "reject")
	if len(a.Skills.List()) != 0 {
		t.Fatal("rejected skill was applied")
	}
	if !contains(events, "skill.resolved") {
		t.Fatalf("skill events: %v", events)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
