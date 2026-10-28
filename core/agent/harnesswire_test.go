package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/harness"
	"github.com/uidbz/anneal/core/permission"
)

func TestSystemPromptLayers(t *testing.T) {
	layers := []harness.Layer{
		{Name: "system", Content: "always run tests"},
		{Name: "guardrails", Content: "never force-push"},
	}
	p := SystemPrompt(t.TempDir(), "build", layers)
	iSys := strings.Index(p, `name="system"`)
	iGr := strings.Index(p, `name="guardrails"`)
	iEnv := strings.Index(p, "<env>")
	if iSys < 0 || iGr < 0 || iEnv < 0 || !(iSys < iGr && iGr < iEnv) {
		t.Fatalf("layer order/env placement:\nsys=%d gr=%d env=%d", iSys, iGr, iEnv)
	}
	if !strings.Contains(p, "always run tests") {
		t.Fatal("layer content missing")
	}
	if strings.Contains(p, "PLAN MODE") {
		t.Fatal("plan text in build mode")
	}
	if !strings.Contains(SystemPrompt(t.TempDir(), "plan", nil), "PLAN MODE") {
		t.Fatal("plan mode text missing")
	}
	p = SystemPrompt(t.TempDir(), "build", nil)
	for _, want := range []string{"GOMODCACHE", "WebFetch", "mcp__gortex__"} {
		if !strings.Contains(p, want) {
			t.Errorf("missing local-source guidance %q", want)
		}
	}
}

func TestHarnessInfoTyped(t *testing.T) {
	root := filepath.Join(t.TempDir(), "harness")
	h := harness.New(root)
	if err := h.Seed(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "system.md"), []byte("be concise"), 0o644)
	os.WriteFile(filepath.Join(root, "guardrails.md"), []byte("deny: Bash(rm -rf *)\n"), 0o644)

	r := &Runner{cfg: &config.Config{Harness: config.Harness{Enabled: true, CharLimit: 2000}}, harn: h}
	info := r.HarnessInfo()
	if info.Root != root || !info.Enabled || len(info.Components) != 1 {
		t.Fatalf("info: %+v", info)
	}
	if len(info.Rules.Deny) != 1 || len(info.Warnings) != 0 {
		t.Fatalf("rules/warnings: %+v", info)
	}
	out := FormatHarnessInfo(info)
	if !strings.Contains(out, "rules: allow=0 ask=0 deny=1") || !strings.Contains(out, "component system: 10 chars") {
		t.Fatalf("formatted: %q", out)
	}

	// No harness store: empty (non-nil) slices, no assertion panic.
	empty := (&Runner{cfg: &config.Config{}}).HarnessInfo()
	if empty.Components == nil || empty.Rules.Allow == nil || empty.Warnings == nil {
		t.Fatalf("nil slices leak into JSON: %+v", empty)
	}
}

func TestPermConfigHarnessRules(t *testing.T) {
	root := filepath.Join(t.TempDir(), "harness")
	h := harness.New(root)
	if err := h.Seed(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "guardrails.md"),
		[]byte("deny: Bash(rm -rf *)\nallow: Bash(go test *)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	rules := permConfig(cfg, h)
	denySeen := false
	allowSeen := false
	for _, r := range rules {
		if r.Effect == permission.Deny && r.Action == "bash" && r.Resource == "rm -rf *" {
			denySeen = true
		}
		if r.Effect == permission.Allow && r.Action == "bash" && r.Resource == "go test *" {
			allowSeen = true
		}
	}
	if !denySeen {
		t.Fatal("harness deny rule missing")
	}
	if !allowSeen {
		t.Fatal("harness allow rule missing")
	}
	// Disabled harness must not mount rules.
	cfg.Harness.Enabled = false
	for _, r := range permConfig(cfg, h) {
		if r.Effect == permission.Deny && r.Action == "bash" && r.Resource == "rm -rf *" {
			t.Fatal("rules mounted while disabled")
		}
	}
}
