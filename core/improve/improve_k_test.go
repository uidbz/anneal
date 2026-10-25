package improve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/probe"

	"github.com/deepnoodle-ai/dive/llm"
)

// iterScripts is one iteration's scripted agent queue:
// editor(inv edits), editor(dom no-op), probe(inv), contrast, composer,
// successor probe, reflector.
func iterScripts(candDir, composeDir, rule, decision string, probeTokens int) []*scriptedLLM {
	succ := respText("successor run")
	if probeTokens > 0 {
		succ = &llm.Response{Role: llm.Assistant, StopReason: "end_turn",
			Usage:   llm.Usage{InputTokens: probeTokens},
			Content: []llm.Content{&llm.TextContent{Text: "successor run"}}}
	}
	return []*scriptedLLM{
		{resps: []*llm.Response{
			respTool("e1", "Write", map[string]any{"file_path": candDir + "/system.md",
				"content": rule}),
			respText("edited")}},
		{resps: []*llm.Response{respText("no domain edits")}},
		{resps: []*llm.Response{
			respTool("p1", "Read", map[string]any{"file_path": "main.go"}),
			respText("probed")}},
		{resps: []*llm.Response{respText("improved: probe verified (quoted); keep")}},
		{resps: []*llm.Response{
			respTool("c1", "Write", map[string]any{"file_path": composeDir + "/system.md",
				"content": rule}),
			respTool("c2", "Write", map[string]any{"file_path": composeDir + "/decisions.md",
				"content": decision}),
			respText("composed")}},
		{resps: []*llm.Response{
			respTool("s1", "Read", map[string]any{"file_path": "main.go"}),
			succ}},
		{resps: []*llm.Response{respText(`## situation
go feature task; verify-before-done deficiency
## lessons
- ` + decision + ` → probe re-read outputs
## evidence
- contrast quoted verification`)}},
	}
}

func TestStepK2Sequential(t *testing.T) {
	cfg, h, src, lib := fixture(t)
	imp := filepath.Join(os.Getenv("ANNEAL_HOME"), "improvements", "k2")
	s1 := iterScripts(filepath.Join(imp, "iter1", "candidates", "invariant"),
		filepath.Join(imp, "iter1", "compose"), "base rule\nRULE-ONE always re-read", "system.md: adopt — quoted verify", 0)
	s2 := iterScripts(filepath.Join(imp, "iter2", "candidates", "invariant"),
		filepath.Join(imp, "iter2", "compose"), "base rule\nRULE-ONE always re-read\nRULE-TWO re-run tests", "system.md: adopt RULE-TWO — quoted test rerun", 0)
	q := &queueLLM{scripts: append(append([]*scriptedLLM{}, s1...), s2...)}
	var events []map[string]any
	res, err := Step(context.Background(), Options{
		Cfg: cfg, Model: q.next, ModelID: "scripted", Harness: h, Workdir: src,
		SessionID: "s1", Task: "edit main.go", RefDigest: "REF digest",
		RunID: "k2", K: 2, Mode: "sequential", Library: lib,
		Emit: func(v any) {
			if e, ok := v.(map[string]any); ok {
				events = append(events, e)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Iterations) != 2 || res.K != 2 || res.Mode != "sequential" {
		t.Fatalf("iterations: %+v k=%d mode=%s", res.Iterations, res.K, res.Mode)
	}
	if res.Iter != 2 || res.Successor != filepath.Join(imp, "iter2", "compose") {
		t.Fatalf("final: iter=%d successor=%s", res.Iter, res.Successor)
	}
	// chaining: iteration 2 starts from iteration 1's successor harness
	i2h0 := filepath.Join(imp, "iter2", "harness0", "system.md")
	b, err := os.ReadFile(i2h0)
	if err != nil || !strings.Contains(string(b), "RULE-ONE") {
		t.Fatalf("iter2 base not chained from iter1 successor: %q %v", b, err)
	}
	// iteration-local record exists; library has two entries (chained steps
	// each carry their own evidence)
	if _, ok := loadStep(filepath.Join(imp, "iter1")); !ok {
		t.Fatal("iter1 record missing")
	}
	if ls := lib.List(); len(ls) != 2 {
		t.Fatalf("library entries: %+v", ls)
	}
	// done event on the multi path
	var done map[string]any
	for _, e := range events {
		if e["type"] == "improve.done" {
			done = e
		}
	}
	if done == nil || done["ok"] != true || done["successor"] != true || done["k"] != 2 {
		t.Fatalf("done event: %v", done)
	}
	// root step.json roundtrips with iterations
	rb, _ := os.ReadFile(filepath.Join(imp, "step.json"))
	var back Result
	if json.Unmarshal(rb, &back) != nil || len(back.Iterations) != 2 {
		t.Fatalf("root step.json: %s", rb)
	}
}

func TestStepK2Resume(t *testing.T) {
	// completes via resume only: run 1 fresh, run 2 resumed (zero scripts)
	cfg, h, src, lib := fixture(t)
	imp := filepath.Join(os.Getenv("ANNEAL_HOME"), "improvements", "kr")
	s1 := iterScripts(filepath.Join(imp, "iter1", "candidates", "invariant"),
		filepath.Join(imp, "iter1", "compose"), "base rule\nRULE-A", "adopt A", 0)
	s2 := iterScripts(filepath.Join(imp, "iter2", "candidates", "invariant"),
		filepath.Join(imp, "iter2", "compose"), "base rule\nRULE-A\nRULE-B", "adopt B", 0)
	q := &queueLLM{scripts: append(append([]*scriptedLLM{}, s1...), s2...)}
	if _, err := Step(context.Background(), Options{
		Cfg: cfg, Model: q.next, ModelID: "scripted", Harness: h, Workdir: src,
		SessionID: "s1", Task: "edit main.go", RefDigest: "REF",
		RunID: "kr", K: 2, Mode: "sequential", Library: lib,
	}); err != nil {
		t.Fatal(err)
	}
	// resume: everything already recorded → zero model calls
	q2 := &queueLLM{}
	res, err := Step(context.Background(), Options{
		Cfg: cfg, Model: q2.next, ModelID: "scripted", Harness: h, Workdir: src,
		SessionID: "s1", Task: "edit main.go", RefDigest: "REF",
		RunID: "kr", K: 2, Mode: "sequential", Library: lib, Resume: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Iterations) != 2 {
		t.Fatalf("iterations: %+v", res.Iterations)
	}
	for _, it := range res.Iterations {
		if it.Skipped != "resumed" {
			t.Fatalf("iter %d not marked resumed: %+v", it.Iter, it)
		}
	}
	if res.Successor != filepath.Join(imp, "iter2", "compose") {
		t.Fatalf("resumed successor: %s", res.Successor)
	}
	if ls := lib.List(); len(ls) != 2 {
		t.Fatalf("resume duplicated entries: %+v", ls)
	}
}

func TestParallelSelect(t *testing.T) {
	cfg, h, src, lib := fixture(t)
	imp := filepath.Join(os.Getenv("ANNEAL_HOME"), "improvements", "kp")
	s1 := iterScripts(filepath.Join(imp, "iter1", "candidates", "invariant"),
		filepath.Join(imp, "iter1", "compose"), "base rule\nRULE-1", "adopt 1", 0)
	s2 := iterScripts(filepath.Join(imp, "iter2", "candidates", "invariant"),
		filepath.Join(imp, "iter2", "compose"), "base rule\nRULE-2", "adopt 2", 0)
	all := append([]*scriptedLLM{}, s1...)
	all = append(all, s2...)
	// selector: picks OPTION B (anonymized → iteration 2 here)
	all = append(all, &scriptedLLM{resps: []*llm.Response{respText("B")}})
	q := &queueLLM{scripts: all}
	var events []map[string]any
	res, err := Step(context.Background(), Options{
		Cfg: cfg, Model: q.next, ModelID: "scripted", Harness: h, Workdir: src,
		SessionID: "s1", Task: "edit main.go", RefDigest: "REF",
		RunID: "kp", K: 2, Mode: "parallel", Library: lib,
		Emit: func(v any) {
			if e, ok := v.(map[string]any); ok {
				events = append(events, e)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Successor != filepath.Join(imp, "iter2", "compose") {
		t.Fatalf("selector kept wrong candidate: %s", res.Successor)
	}
	var sel map[string]any
	for _, e := range events {
		if e["type"] == "improve.selector" {
			sel = e
		}
	}
	if sel == nil || sel["chosen_iter"] != 2 || sel["label"] != "B" {
		t.Fatalf("selector event: %v", sel)
	}
	// both parallel iterations ran from the SAME base (no chaining)
	a, _ := os.ReadFile(filepath.Join(imp, "iter1", "harness0", "system.md"))
	b, _ := os.ReadFile(filepath.Join(imp, "iter2", "harness0", "system.md"))
	if string(a) != string(b) || string(a) != "base rule" {
		t.Fatalf("parallel bases differ: %q vs %q", a, b)
	}
}

func TestSelectorNoneKeepsLast(t *testing.T) {
	q := &queueLLM{scripts: []*scriptedLLM{{resps: []*llm.Response{respText("NONE")}}}}
	opts := &Options{
		Cfg: config.Default(), Model: q.next, Task: "t", RefDigest: "ref",
		Emit: func(any) {},
	}
	sub := []*Result{
		{Iter: 1, Successor: "a", SuccessorProbe: &probe.Record{Name: "a", Task: "t"}, Decisions: "one"},
		{Iter: 2, Successor: "b", SuccessorProbe: &probe.Record{Name: "b", Task: "t"}, Decisions: "two"},
	}
	if got := selectSuccessor(context.Background(), opts, "run", sub); got.Iter != 2 {
		t.Fatalf("NONE kept iter %d, want last (2)", got.Iter)
	}
}

func TestReviewerHintsSeedPrompts(t *testing.T) {
	hb := hintsBlock([]string{"done without rerunning tests"})
	if !strings.Contains(hb, "REVIEWER FLAGS") || !strings.Contains(hb, "done without rerunning tests") {
		t.Fatalf("block: %q", hb)
	}
	if !strings.Contains(editorTask("t", "r", "/d", "LIB"+hb), "REVIEWER FLAGS") {
		t.Fatal("editor task lacks flags")
	}
	if !strings.Contains(composerTask("t", "r", "blocks", "/d", "LIB"+hb), "REVIEWER FLAGS") {
		t.Fatal("composer task lacks flags")
	}
}

func TestTokenBudgetStopsChain(t *testing.T) {
	cfg, h, src, lib := fixture(t)
	imp := filepath.Join(os.Getenv("ANNEAL_HOME"), "improvements", "kb")
	s1 := iterScripts(filepath.Join(imp, "iter1", "candidates", "invariant"),
		filepath.Join(imp, "iter1", "compose"), "base rule\nRULE-X", "adopt X", 5000)
	q := &queueLLM{scripts: s1} // deliberately no iter-2 scripts
	var events []map[string]any
	res, err := Step(context.Background(), Options{
		Cfg: cfg, Model: q.next, ModelID: "scripted", Harness: h, Workdir: src,
		SessionID: "s1", Task: "edit main.go", RefDigest: "REF",
		RunID: "kb", K: 3, Mode: "sequential", Library: lib,
		MaxTotalTokens: 100,
		Hints:          []string{"declares done without re-running tests"},
		Emit: func(v any) {
			if e, ok := v.(map[string]any); ok {
				events = append(events, e)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Iterations) != 1 || !strings.Contains(res.Note, "token budget") {
		t.Fatalf("iterations=%d note=%q", len(res.Iterations), res.Note)
	}
	if len(res.Hints) != 1 || !strings.Contains(res.Hints[0], "re-running tests") {
		t.Fatalf("hints not recorded: %+v", res.Hints)
	}
	var budget map[string]any
	for _, e := range events {
		if e["type"] == "improve.budget" {
			budget = e
		}
	}
	if budget == nil || budget["iter"] != 2 {
		t.Fatalf("budget event: %v", budget)
	}
	if _, err := os.Stat(filepath.Join(imp, "iter2")); !os.IsNotExist(err) {
		t.Fatal("iter2 ran despite budget")
	}
	// library still recorded the one completed iteration
	if ls := lib.List(); len(ls) != 1 {
		t.Fatalf("library: %+v", ls)
	}
}
