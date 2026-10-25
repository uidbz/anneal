package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyagent/core/config"
	"fyagent/core/harness"

	"github.com/deepnoodle-ai/dive/permission"
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
	c := permConfig(cfg, h)
	denyMsg := ""
	allowSeen := false
	for _, r := range c.Rules {
		if r.Type == permission.RuleDeny && r.Tool == "Bash" && r.Specifier == "rm -rf *" {
			denyMsg = r.Message
		}
		if r.Type == permission.RuleAllow && r.Tool == "Bash" && r.Specifier == "go test *" {
			allowSeen = true
		}
	}
	if denyMsg != "denied by harness guardrail" {
		t.Fatalf("harness deny rule missing/wrong: %q", denyMsg)
	}
	if !allowSeen {
		t.Fatal("harness allow rule missing")
	}
	// Disabled harness must not mount rules.
	cfg.Harness.Enabled = false
	for _, r := range permConfig(cfg, h).Rules {
		if r.Message == "denied by harness guardrail" {
			t.Fatal("rules mounted while disabled")
		}
	}
}
