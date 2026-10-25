package toolset

import (
	"context"
	"strings"
	"testing"

	"fyagent/core/config"
	"fyagent/core/memory"
	"fyagent/core/skills"
)

func TestMemoryToolApplyAndStage(t *testing.T) {
	t.Setenv("FYAGENT_HOME", t.TempDir())
	cfg := &config.Memory{Enabled: true, MemoryCharLimit: 300, UserCharLimit: 120}
	store := memory.NewStore(cfg)

	var applied, staged int
	tool := MemoryTool(store, "agent", MemoryCallbacks{
		Apply: func(op *memory.Op, content string) { applied++ },
		Stage: func(_, _ *memory.Op) { staged++ },
	})

	res, err := tool.Call(context.Background(), map[string]any{
		"target": "user", "action": "add", "content": "prefers tabs",
	})
	if err != nil || res.IsError {
		t.Fatalf("add: %v %+v", err, res)
	}
	if applied != 1 || staged != 0 {
		t.Fatalf("callbacks: applied=%d staged=%d", applied, staged)
	}
	if cur, _ := store.Read("user"); cur != "- prefers tabs\n" {
		t.Fatalf("memory: %q", cur)
	}

	// Same tool instance obeys a live write_approval flip.
	cfg.WriteApproval = true
	res, err = tool.Call(context.Background(), map[string]any{
		"target": "memory", "action": "add", "content": "uses pnpm",
	})
	if err != nil || res.IsError {
		t.Fatalf("stage: %v %+v", err, res)
	}
	if staged != 1 || applied != 1 {
		t.Fatalf("callbacks after flip: applied=%d staged=%d", applied, staged)
	}
	if cur, _ := store.Read("memory"); cur != "" {
		t.Fatalf("staged write applied directly: %q", cur)
	}
	pending, _ := store.ListPending()
	if len(pending) != 1 || pending[0].Source != "agent" {
		t.Fatalf("pending: %+v", pending)
	}
}

func TestSkillToolLifecycle(t *testing.T) {
	t.Setenv("FYAGENT_HOME", t.TempDir())
	store := skills.NewStore(&config.Skills{Enabled: true})

	var appliedActions []string
	tool := SkillTool(store, "review", SkillCallbacks{
		Apply: func(op *skills.Op, result string) { appliedActions = append(appliedActions, op.Action) },
	})

	res, err := tool.Call(context.Background(), map[string]any{
		"action": "create", "name": "go-test-fixture",
		"description": "how to run the flaky suite",
		"content":     "## Steps\n1. run make test",
	})
	if err != nil || res.IsError {
		t.Fatalf("create: %v %+v", err, res)
	}
	if len(appliedActions) != 1 || appliedActions[0] != "create" {
		t.Fatalf("apply callbacks: %v", appliedActions)
	}
	view, err := store.View("go-test-fixture")
	if err != nil || !strings.Contains(view, "run make test") {
		t.Fatalf("view: %q %v", view, err)
	}

	res, err = tool.Call(context.Background(), map[string]any{"action": "list"})
	if err != nil || res.IsError {
		t.Fatalf("list: %v %+v", err, res)
	}
	var listed string
	for _, c := range res.Content {
		listed += c.Text
	}
	if !strings.Contains(listed, "go-test-fixture") {
		t.Fatalf("list output: %q", listed)
	}

	if _, err := tool.Call(context.Background(), map[string]any{
		"action": "patch", "name": "go-test-fixture",
		"old": "make test", "new": "make test -count=1",
	}); err != nil {
		t.Fatal(err)
	}
	view, _ = store.View("go-test-fixture")
	if !strings.Contains(view, "-count=1") {
		t.Fatalf("patch not applied: %q", view)
	}
}
