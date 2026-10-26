package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/anneal/core/config"
)

func TestScriptToolSmoke(t *testing.T) {
	t.Setenv("ANNEAL_HOME", filepath.Join(t.TempDir(), "anneal"))
	wd := t.TempDir()
	r := &Runner{cfg: &config.Config{Agent: config.Agent{Script: true}}, cwd: wd}

	tool := r.scriptTool()
	if tool.Name() != "script" {
		t.Fatalf("tool name = %q", tool.Name())
	}
	if ro := tool.Annotations().ReadOnlyHint; ro {
		t.Fatal("script must not be read-only: plan mode must deny it")
	}

	res, err := tool.Call(context.Background(), []byte(`{"script": "print(\"hi\")\n[1, 2].map(x => x * 2)"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", res.Content[0].Text)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "output:\nhi") || !strings.Contains(text, "result: [2,4]") {
		t.Errorf("unexpected result text: %q", text)
	}

	// Denied module comes back as tool output text, not a harness error.
	res, err = tool.Call(context.Background(), []byte(`{"script": "require([\"@exec\"])", "modules": ["exec"]}`))
	if err != nil || res.IsError {
		t.Fatalf("call: %v (isError=%v)", err, res.IsError)
	}
	if !strings.Contains(res.Content[0].Text, "@exec is not available") {
		t.Errorf("want module denial text, got %q", res.Content[0].Text)
	}

	// fs writes land in the workdir.
	res, err = tool.Call(context.Background(), []byte(`{"script": "fs.write(\"tool.txt\", \"from script\")"}`))
	if err != nil || res.IsError {
		t.Fatalf("write call: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(wd, "tool.txt"))
	if err != nil || string(data) != "from script" {
		t.Fatalf("fs.write did not land: %v %q", err, data)
	}
}

func TestScriptToolConfigDefault(t *testing.T) {
	cfg := config.Default()
	if !cfg.Agent.Script {
		t.Fatal("agent.script should default to true")
	}
	var found bool
	for _, r := range cfg.Agent.Ask {
		if r == "script" {
			found = true
		}
	}
	if !found {
		t.Errorf("script should be ask-by-default, got %v", cfg.Agent.Ask)
	}
}
