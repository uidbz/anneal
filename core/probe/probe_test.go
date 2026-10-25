package probe

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"fyagent/core/config"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

func diveHC(name string, input []byte) *dive.HookContext {
	return &dive.HookContext{Call: &llm.ToolUseContent{Name: name, Input: input}}
}

func TestPrepareCopiesWithExclusions(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "main.go"), []byte("package main"), 0o644)
	os.MkdirAll(filepath.Join(src, ".git"), 0o755)
	os.WriteFile(filepath.Join(src, ".git", "HEAD"), []byte("ref: refs/heads/main"), 0o644)
	os.MkdirAll(filepath.Join(src, "node_modules", "pkg"), 0o755)
	os.WriteFile(filepath.Join(src, "node_modules", "pkg", "index.js"), []byte("junk"), 0o644)
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "sub", "x.txt"), []byte("x"), 0o600)

	root := filepath.Join(t.TempDir(), "sb")
	s, err := Prepare(src, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.WS, "main.go")); err != nil {
		t.Fatal("main.go not copied")
	}
	if _, err := os.Stat(filepath.Join(s.WS, ".git", "HEAD")); err != nil {
		t.Fatal(".git not copied (fidelity)")
	}
	if _, err := os.Stat(filepath.Join(s.WS, "sub", "x.txt")); err != nil {
		t.Fatal("nested file not copied")
	}
	if _, err := os.Stat(filepath.Join(s.WS, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("node_modules should be skipped")
	}
	// original untouched
	if _, err := os.Stat(filepath.Join(src, "node_modules")); err != nil {
		t.Fatal("Prepare modified the original tree")
	}
}

func jailCheck(t *testing.T, s *Sandbox, tool string, input map[string]any) error {
	t.Helper()
	raw, _ := json.Marshal(input)
	h := s.Jail()
	return h(context.Background(), diveHC(tool, raw))
}

func TestJailTable(t *testing.T) {
	t.Setenv("FYAGENT_HOME", t.TempDir())
	src := t.TempDir()
	s, err := Prepare(src, filepath.Join(t.TempDir(), "sb"))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "victim.txt")

	cases := []struct {
		name     string
		tool     string
		input    map[string]any
		wantDeny bool
	}{
		{"write inside", "Write", map[string]any{"file_path": s.WS + "/a.txt", "content": "x"}, false},
		{"write outside", "Write", map[string]any{"file_path": outside, "content": "x"}, true},
		{"write relative", "Edit", map[string]any{"file_path": "a.txt"}, true},
		{"write into fyagent state", "Write", map[string]any{"file_path": filepath.Join(os.Getenv("FYAGENT_HOME"), "config.json"), "content": "x"}, true},
		{"read inside", "Read", map[string]any{"file_path": s.WS + "/main.go"}, false},
		{"read original project", "Read", map[string]any{"file_path": filepath.Join(src, "main.go")}, true},
		{"read system", "Read", map[string]any{"file_path": "/usr/bin/env"}, false},
		{"bash inside", "Bash", map[string]any{"command": "go build ./...", "working_directory": s.WS}, false},
		{"bash no wd", "Bash", map[string]any{"command": "go build"}, true},
		{"bash wd outside", "Bash", map[string]any{"command": "ls", "working_directory": "/"}, true},
		{"bash rm original", "Bash", map[string]any{"command": "rm -rf " + src, "working_directory": s.WS}, true},
		{"bash glob original", "Bash", map[string]any{"command": "cat " + src + "/*.go", "working_directory": s.WS}, true},
		{"bash touch state", "Bash", map[string]any{"command": "echo x > " + filepath.Join(os.Getenv("FYAGENT_HOME"), "evil"), "working_directory": s.WS}, true},
		{"bash system bin", "Bash", map[string]any{"command": "/usr/bin/env go vet", "working_directory": s.WS}, false},
		{"bash write sandbox abs", "Bash", map[string]any{"command": "echo hi > " + s.WS + "/out.txt", "working_directory": s.WS}, false},
	}
	for _, tc := range cases {
		err := jailCheck(t, s, tc.tool, tc.input)
		if tc.wantDeny && err == nil {
			t.Errorf("%s: expected deny, got allow", tc.name)
		}
		if !tc.wantDeny && err != nil {
			t.Errorf("%s: expected allow, got deny: %v", tc.name, err)
		}
	}
}

type scriptedLLM struct {
	mu    sync.Mutex
	resps []*llm.Response
	calls int
}

func (f *scriptedLLM) Name() string { return "scripted" }

func (f *scriptedLLM) Generate(ctx context.Context, opts ...llm.Option) (*llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls >= len(f.resps) {
		return &llm.Response{Role: llm.Assistant, StopReason: "end_turn",
			Content: []llm.Content{&llm.TextContent{Text: "done"}}}, nil
	}
	r := f.resps[f.calls]
	f.calls++
	return r, nil
}

func toolCallResp(id, name string, input map[string]any) *llm.Response {
	raw, _ := json.Marshal(input)
	return &llm.Response{Role: llm.Assistant, StopReason: "tool_use",
		Content: []llm.Content{&llm.ToolUseContent{ID: id, Name: name, Input: raw}}}
}

func TestRunRecordsAndJails(t *testing.T) {
	t.Setenv("FYAGENT_HOME", t.TempDir())
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "main.go"), []byte("package main"), 0o644)
	s, err := Prepare(src, filepath.Join(t.TempDir(), "sb"))
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim.txt")
	os.WriteFile(victim, []byte("keep"), 0o644)

	fake := &scriptedLLM{resps: []*llm.Response{
		toolCallResp("t1", "Write", map[string]any{"file_path": s.WS + "/new.txt", "content": "written in sandbox"}),
		toolCallResp("t2", "Write", map[string]any{"file_path": victim, "content": "should be blocked"}),
		toolCallResp("t3", "Bash", map[string]any{"command": "cat new.txt", "working_directory": s.WS}),
	}}
	cfg := config.Default()
	out := t.TempDir()
	rec, err := Run(context.Background(), Options{
		Name: "probe-x", Cfg: cfg,
		Model:   func() llm.LLM { return fake },
		ModelID: "scripted", System: "you are a probe", Task: "write a file; then try outside",
		Scratch: s, OutDir: out, HarnessHash: "abc", MaxIterations: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	// in-sandbox write applied
	if b, err := os.ReadFile(s.WS + "/new.txt"); err != nil || string(b) != "written in sandbox" {
		t.Fatalf("sandbox write not applied: %v", err)
	}
	// jailed write blocked, original intact
	if b, err := os.ReadFile(victim); err != nil || string(b) != "keep" {
		t.Fatalf("jail failed: %q %v", b, err)
	}
	if len(rec.Steps) != 3 {
		t.Fatalf("steps: %+v", rec.Steps)
	}
	if !rec.Steps[1].Error || !strings.Contains(rec.Steps[1].Preview, "probe jail") {
		t.Fatalf("step 2 should be a jail error: %+v", rec.Steps[1])
	}
	if rec.Steps[2].Error || !strings.Contains(rec.Steps[2].Preview, "written in sandbox") {
		t.Fatalf("step 3 bash output wrong: %+v", rec.Steps[2])
	}
	// record file written + digest renders
	b, err := os.ReadFile(filepath.Join(out, "probe-x.json"))
	if err != nil {
		t.Fatal(err)
	}
	var back Record
	if json.Unmarshal(b, &back) != nil || back.Name != "probe-x" || len(back.Steps) != 3 {
		t.Fatalf("record roundtrip: %s", b)
	}
	if rec.ConfigHash == "" || rec.HarnessHash != "abc" || rec.DurationS <= 0 {
		t.Fatalf("audit fields: %+v", rec)
	}
	if d := rec.Digest(5000); !strings.Contains(d, "Write") || !strings.Contains(d, "deliverable") {
		t.Fatalf("digest: %q", d)
	}
	if d := rec.Digest(50); len(d) > 200 {
		t.Fatalf("digest not bounded: %d", len(d))
	}
}

func TestConfigHashStability(t *testing.T) {
	cfg := config.Default()
	h1 := ConfigHash(cfg, "m1", 40)
	h2 := ConfigHash(cfg, "m1", 40)
	h3 := ConfigHash(cfg, "m2", 40)
	if h1 != h2 || h1 == h3 || len(h1) != 16 {
		t.Fatalf("hashes: %s %s %s", h1, h2, h3)
	}
	if HarnessHash([]LayerContent{{"system", "a"}}) != HarnessHash([]LayerContent{{"system", "a"}}) {
		t.Fatal("harness hash unstable")
	}
}
