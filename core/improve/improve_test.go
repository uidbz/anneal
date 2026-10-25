package improve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"fyagent/core/config"
	"fyagent/core/harness"
	"fyagent/core/harnesslib"
	"fyagent/core/probe"

	"github.com/deepnoodle-ai/dive/llm"
)

// queueLLM pops one scripted agent per Model() call, in stage order:
// editor(invariant), editor(domain), [probe, contrast]…, composer, [successor probe].
type queueLLM struct {
	mu      sync.Mutex
	scripts []*scriptedLLM
	idx     int
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

func (q *queueLLM) next() llm.LLM {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.idx >= len(q.scripts) {
		return &scriptedLLM{}
	}
	s := q.scripts[q.idx]
	q.idx++
	return s
}

func respTool(id, name string, input map[string]any) *llm.Response {
	raw, _ := json.Marshal(input)
	return &llm.Response{Role: llm.Assistant, StopReason: "tool_use",
		Content: []llm.Content{&llm.ToolUseContent{ID: id, Name: name, Input: raw}}}
}

func respText(text string) *llm.Response {
	return &llm.Response{Role: llm.Assistant, StopReason: "end_turn",
		Content: []llm.Content{&llm.TextContent{Text: text}}}
}

func fixture(t *testing.T) (*config.Config, *harness.Store, string, *harnesslib.Store) {
	t.Helper()
	t.Setenv("FYAGENT_HOME", t.TempDir())
	cfg := config.Default()
	h := harness.New(filepath.Join(os.Getenv("FYAGENT_HOME"), "harness"))
	if err := h.Seed(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(h.Root(), "system.md"), []byte("base rule"), 0o644)
	lib := harnesslib.New(filepath.Join(os.Getenv("FYAGENT_HOME"), "harness-lib"))
	if err := lib.Seed(); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n"), 0o644)
	return cfg, h, src, lib
}

func TestExecutionEvidencePredicate(t *testing.T) {
	if hasExecutionEvidence(&Result{}) {
		t.Fatal("empty step admitted")
	}
	emptyProbe := &Result{Candidates: []Candidate{{Probe: &probe.Record{}}}}
	if hasExecutionEvidence(emptyProbe) {
		t.Fatal("probe with no executed steps admitted")
	}
	step := []probe.ToolStep{{Name: "Read", Input: "main.go"}}
	if !hasExecutionEvidence(&Result{Candidates: []Candidate{{Probe: &probe.Record{Steps: step}}}}) {
		t.Fatal("candidate evidence rejected")
	}
	if !hasExecutionEvidence(&Result{SuccessorProbe: &probe.Record{Steps: step}}) {
		t.Fatal("successor evidence rejected")
	}
}

func TestStepNoEditsBails(t *testing.T) {
	cfg, h, src, lib := fixture(t)
	q := &queueLLM{scripts: []*scriptedLLM{
		{resps: []*llm.Response{respText("no changes")}},
		{resps: []*llm.Response{respText("no changes")}},
	}}
	var types []string
	res, err := Step(context.Background(), Options{
		Cfg: cfg, Model: q.next, ModelID: "scripted", Harness: h, Workdir: src,
		SessionID: "s1", Task: "do the thing", RefDigest: "ref digest",
		Library: lib,
		Emit: func(v any) {
			if e, ok := v.(map[string]any); ok {
				types = append(types, e["type"].(string))
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 2 || res.Candidates[0].Skipped != "no edits" {
		t.Fatalf("candidates: %+v", res.Candidates)
	}
	if res.Successor != "" {
		t.Fatal("unexpected successor")
	}
	// step.json written
	if _, err := os.ReadFile(filepath.Join(res.Dir, "step.json")); err != nil {
		t.Fatal(err)
	}
	var doneErr string
	for _, e := range types {
		if e == "improve.done" {
			doneErr = "seen"
		}
	}
	if doneErr != "seen" {
		t.Fatalf("events: %v", types)
	}
	if len(lib.List()) != 0 {
		t.Fatal("library entry created for a bailed step")
	}
}

func TestStepFullFlow(t *testing.T) {
	cfg, h, src, lib := fixture(t)
	imp := filepath.Join(os.Getenv("FYAGENT_HOME"), "improvements", "t1")
	candDir := filepath.Join(imp, "candidates", "invariant")
	composeDir := filepath.Join(imp, "compose")
	q := &queueLLM{scripts: []*scriptedLLM{
		// editor invariant: adds a verification rule
		{resps: []*llm.Response{
			respTool("e1", "Write", map[string]any{"file_path": candDir + "/system.md",
				"content": "base rule\nalways re-read produced files before done"}),
			respText("changed system.md for unverified-done")}},
		// editor domain: no edits
		{resps: []*llm.Response{respText("nothing domain-specific")}},
		// probe (invariant): reads with relative path (workspace-jailed)
		{resps: []*llm.Response{
			respTool("p1", "Read", map[string]any{"file_path": "main.go"}),
			respText("verified and done")}},
		// contrast note (invariant)
		{resps: []*llm.Response{respText("improved: probe re-read main.go (quoted); keep")}},
		// composer: adopt the rule + write decisions.md
		{resps: []*llm.Response{
			respTool("c1", "Write", map[string]any{"file_path": composeDir + "/system.md",
				"content": "base rule\nalways re-read produced files before done"}),
			respTool("c2", "Write", map[string]any{"file_path": composeDir + "/decisions.md",
				"content": "system.md: adopt — contrast: probe re-read main.go"}),
			respText("composed")}},
		// successor probe
		{resps: []*llm.Response{
			respTool("s1", "Read", map[string]any{"file_path": "main.go"}),
			respText("successor run")}},
		// reflector
		{resps: []*llm.Response{respText(`## situation
small Go feature addition; unverified-done deficiency
## lessons
- verify rule → successor run re-read produced files
## evidence
- contrast: probe re-read main.go (quoted)
- decisions: system.md adopt`)}},
	}}
	var events []map[string]any
	res, err := Step(context.Background(), Options{
		Cfg: cfg, Model: q.next, ModelID: "scripted", Harness: h, Workdir: src,
		SessionID: "s1", Task: "edit main.go", RefDigest: "REF: declared done without re-reading",
		RunID: "t1", Library: lib,
		Emit: func(v any) {
			if e, ok := v.(map[string]any); ok {
				events = append(events, e)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	inv := res.Candidates[0]
	if inv.Kind != "invariant" || inv.Skipped != "" || !strings.Contains(inv.Diff, "re-read") {
		t.Fatalf("invariant candidate: %+v", inv)
	}
	if inv.Probe == nil || !strings.Contains(inv.Contrast, "improved") {
		t.Fatalf("missing probe/contrast: %+v", inv)
	}
	// Audit hashes must be present on probe records, not just the step.
	if inv.Probe.HarnessHash == "" {
		t.Fatal("candidate probe record lacks harness hash")
	}
	if res.SuccessorProbe == nil || res.SuccessorProbe.HarnessHash == "" {
		t.Fatal("successor probe record lacks harness hash")
	}
	if res.Candidates[1].Skipped != "no edits" {
		t.Fatalf("domain candidate: %+v", res.Candidates[1])
	}
	// successor exists, decisions recorded, decisions.md excluded from layers
	if res.Successor == "" {
		t.Fatal("no successor")
	}
	if !strings.Contains(res.Decisions, "adopt") {
		t.Fatalf("decisions: %q", res.Decisions)
	}
	layers := harness.LayersFromDir(res.Successor, 6000)
	for _, l := range layers {
		if l.Name == "decisions" {
			t.Fatal("decisions.md leaked into layers")
		}
	}
	if len(layers) != 1 || layers[0].Name != "system" || layers[0].Content == "" {
		t.Fatalf("layers: %+v", layers)
	}
	// successor probe ran
	if res.SuccessorProbe == nil {
		t.Fatal("no successor probe")
	}
	// events contract: keys always present
	var sawDone map[string]any
	for _, e := range events {
		if e["type"] == "improve.done" {
			sawDone = e
		}
	}
	if sawDone == nil || sawDone["ok"] != true || sawDone["successor"] != true || sawDone["error"] != "" {
		t.Fatalf("done event: %v", sawDone)
	}
	// step.json roundtrips and hashes exist
	b, err := os.ReadFile(filepath.Join(res.Dir, "step.json"))
	if err != nil {
		t.Fatal(err)
	}
	var back Result
	if json.Unmarshal(b, &back) != nil || back.HarnessHash == "" || back.RunID != res.RunID {
		t.Fatalf("step.json: %s", b)
	}
	// sandboxes stayed inside the improvement dir, src untouched
	if _, err := os.Stat(filepath.Join(src, "main.go")); err != nil {
		t.Fatal(err)
	}
	// harnessing-level recursion: evidence-supported entry in the library
	if res.LibraryEntry == "" {
		t.Fatal("no library entry")
	}
	ls := lib.List()
	if len(ls) != 1 || !strings.Contains(ls[0].Situation, "unverified") {
		t.Fatalf("lib list: %+v", ls)
	}
	idxB, err := os.ReadFile(filepath.Join(os.Getenv("FYAGENT_HOME"), "harness-lib", "INDEX.md"))
	if err != nil || !strings.Contains(string(idxB), res.LibraryEntry) {
		t.Fatalf("index: %s", idxB)
	}
	if ctxs := lib.Context("edit main.go verify files", 2, 2000); !strings.Contains(ctxs, "verify rule") {
		t.Fatalf("lib context: %q", ctxs)
	}
	snap := filepath.Join(os.Getenv("FYAGENT_HOME"), "harness-lib", res.LibraryEntry, "harness", "system.md")
	if b, err := os.ReadFile(snap); err != nil || !strings.Contains(string(b), "re-read") {
		t.Fatalf("entry snapshot: %v", err)
	}
	// The fast path must report its recursion parameters.
	if res.K != 1 || res.Mode != "sequential" {
		t.Fatalf("k/mode: k=%d mode=%q", res.K, res.Mode)
	}
	if got := tokens(&Result{StageTokens: 100, Candidates: []Candidate{{Probe: &probe.Record{Tin: 7, Tout: 3}}}}); got != 110 {
		t.Fatalf("token accounting: %d", got)
	}
}
