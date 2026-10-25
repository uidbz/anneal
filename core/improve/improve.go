// Package improve implements fyagent's task-level recursion: the contrastive
// Propose–Probe–Compose step from Harness² (arXiv 2610.14224, §2.2) at k=1.
// It refines the CURRENT task's harness without ground truth: the finished
// session is the reference execution; two scoped editors propose candidate
// harnesses; each candidate is replayed in a sandbox and behaviorally
// contrasted; a composer merges supported edits into a successor, tied to
// observations. The successor applies as a session-scoped overlay — the
// global harness only changes through the user's promote action (M13+).
package improve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyagent/core/agent"
	"fyagent/core/config"
	"fyagent/core/harness"
	"fyagent/core/paths"
	"fyagent/core/probe"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/toolkit"
)

const kinds = 2 // invariant + domain

// Options configures one contrastive step Φ(q, ℋ, ℰ; ℛ).
type Options struct {
	Cfg       *config.Config
	Model     func() llm.LLM // called once per agent/session
	ModelID   string
	Harness   *harness.Store
	Workdir   string // src workspace (sandbox source)
	SessionID string
	Task      string
	RefDigest string // bounded digest of the reference execution
	Emit      func(any)

	MaxDigestChars   int    // default 12000
	ProbeIterations  int    // default 25
	EditorIterations int    // default 12
	WallTimeoutS     int    // per probe, default 300
	RunID            string // override the timestamp run id (tests)
}

// Candidate is one proposed harness plus its probe evidence.
type Candidate struct {
	Kind     string        `json:"kind"`
	Dir      string        `json:"dir"`
	Diff     string        `json:"diff"`
	Contrast string        `json:"contrast,omitempty"`
	Probe    *probe.Record `json:"probe,omitempty"`
	Skipped  string        `json:"skipped,omitempty"`
}

// Result is the step record ξ (written to <Dir>/step.json too).
type Result struct {
	RunID          string        `json:"run_id"`
	Dir            string        `json:"dir"`
	SessionID      string        `json:"session_id"`
	Task           string        `json:"task"`
	HarnessHash    string        `json:"harness_hash"`
	Candidates     []Candidate   `json:"candidates"`
	Decisions      string        `json:"decisions"`
	Successor      string        `json:"successor,omitempty"`
	SuccessorProbe *probe.Record `json:"successor_probe,omitempty"`
	FinishedAt     time.Time     `json:"finished_at"`
}

func clip(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Step runs one contrastive improvement step. Every stage is recorded; the
// returned Result is complete even when the step bails early (e.g. no
// candidate edited anything).
func Step(ctx context.Context, opts Options) (*Result, error) {
	if opts.Task == "" || opts.RefDigest == "" {
		return nil, fmt.Errorf("improve: task and reference digest required")
	}
	if opts.MaxDigestChars <= 0 {
		opts.MaxDigestChars = 12000
	}
	if opts.ProbeIterations <= 0 {
		opts.ProbeIterations = 25
	}
	if opts.EditorIterations <= 0 {
		opts.EditorIterations = 12
	}
	if opts.WallTimeoutS <= 0 {
		opts.WallTimeoutS = 300
	}
	emit := opts.Emit
	if emit == nil {
		emit = func(any) {}
	}
	limit := opts.Cfg.Harness.CharLimit

	runid := opts.RunID
	if runid == "" {
		runid = time.Now().UTC().Format("20060102-150405")
	}
	dir := filepath.Join(paths.Improvements(), runid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	res := &Result{RunID: runid, Dir: dir, SessionID: opts.SessionID, Task: opts.Task}

	// ℋ snapshot as component files (the base every candidate starts from)
	h0 := filepath.Join(dir, "harness0")
	var layers []harness.Layer
	if opts.Harness != nil && opts.Cfg.Harness.Enabled {
		layers = opts.Harness.Layers(limit)
	}
	res.HarnessHash = probe.HarnessHash(layerContents(layers))
	if err := writeLayers(h0, layers); err != nil {
		return nil, err
	}

	emit(ev("improve.start", runid, map[string]any{"session": opts.SessionID, "harness_hash": res.HarnessHash}))

	// Propose: two scoped editors, one sandboxed candidate dir each.
	names := [kinds]string{"invariant", "domain"}
	for _, kind := range names {
		c := Candidate{Kind: kind, Dir: filepath.Join(dir, "candidates", kind)}
		emit(ev("improve.propose", runid, map[string]any{"kind": kind}))
		if err := copyFiles(h0, c.Dir); err != nil {
			c.Skipped = err.Error()
			res.Candidates = append(res.Candidates, c)
			continue
		}
		if err := runEditor(ctx, opts, kind, c.Dir, editorTask(opts.Task, opts.RefDigest, c.Dir)); err != nil {
			c.Skipped = "editor: " + err.Error()
			res.Candidates = append(res.Candidates, c)
			continue
		}
		c.Diff = dirDiff(h0, c.Dir, 6000)
		if strings.TrimSpace(c.Diff) == "" {
			c.Skipped = "no edits"
		}
		res.Candidates = append(res.Candidates, c)
	}

	// Probe + contrast per candidate.
	for i := range res.Candidates {
		c := &res.Candidates[i]
		if c.Skipped != "" {
			continue
		}
		sb, err := probe.Prepare(opts.Workdir, filepath.Join(dir, "probes", c.Kind))
		if err != nil {
			c.Skipped = "sandbox: " + err.Error()
			continue
		}
		task := opts.Task
		if opts.Workdir != "" {
			task = strings.ReplaceAll(task, opts.Workdir, sb.WS)
		}
		sys := agent.SystemPrompt(sb.WS, "default", append(
			harness.LayersFromDir(c.Dir, limit),
			harness.Layer{Name: "probe-note", Content: probeNote(sb.WS)}))
		rec, runErr := probe.Run(ctx, probe.Options{
			Name: "probe-" + c.Kind, Cfg: opts.Cfg, ModelID: opts.ModelID, Model: opts.Model,
			System: sys, Task: task, Scratch: sb,
			OutDir:        filepath.Join(dir, "probes"),
			MaxIterations: opts.ProbeIterations, WallTimeoutS: opts.WallTimeoutS, Emit: emit,
		})
		c.Probe = rec
		emit(ev("improve.probe", runid, map[string]any{"kind": c.Kind,
			"ok": runErr == nil, "steps": len(rec.Steps), "duration_s": int(rec.DurationS),
			"error": errStr(runErr)}))
		note, err := generate(ctx, opts, "You write terse, evidence-quoted behavioral contrast notes. No scores.",
			contrastPrompt(c.Kind, opts.Task, opts.RefDigest, c.Diff, rec.Digest(opts.MaxDigestChars)))
		if err != nil {
			c.Skipped = "contrast: " + err.Error()
			continue
		}
		c.Contrast = note
	}

	// Compose from candidates that carry probe + contrast evidence.
	var blocks []string
	for _, c := range res.Candidates {
		if c.Skipped != "" {
			continue
		}
		blocks = append(blocks, fmt.Sprintf("CANDIDATE %s — harness changes:\n%s\n\nCONTRAST NOTE:\n%s\n",
			c.Kind, c.Diff, clip(c.Contrast, 2500)))
	}
	if len(blocks) == 0 {
		emit(ev("improve.done", runid, map[string]any{"ok": false,
			"error": "no candidate produced probed edits", "decisions": "", "successor": false}))
		return res, writeStep(dir, res)
	}
	composeDir := filepath.Join(dir, "compose")
	if err := copyFiles(h0, composeDir); err != nil {
		return res, writeStep(dir, res)
	}
	composerErr := runComposer(ctx, opts, composeDir,
		composerTask(opts.Task, opts.RefDigest, strings.Join(blocks, "\n"), composeDir))
	if composerErr != nil {
		emit(ev("improve.done", runid, map[string]any{"ok": false,
			"error": "composer: " + composerErr.Error(), "decisions": "", "successor": false}))
		return res, writeStep(dir, res)
	}
	res.Decisions = readFile(filepath.Join(composeDir, "decisions.md"))
	changed := strings.TrimSpace(dirDiff(h0, composeDir, 60000))
	if changed == "" {
		emit(ev("improve.done", runid, map[string]any{"ok": true, "error": "",
			"decisions": clip(res.Decisions, 800), "successor": false, "note": "composer kept the current harness"}))
		return res, writeStep(dir, res)
	}
	res.Successor = composeDir
	emit(ev("improve.compose", runid, map[string]any{"changed": changedFiles(h0, composeDir)}))

	// Successor execution e′ (evidence for the library step, M13).
	sb, err := probe.Prepare(opts.Workdir, filepath.Join(dir, "successor"))
	if err == nil {
		task := opts.Task
		if opts.Workdir != "" {
			task = strings.ReplaceAll(task, opts.Workdir, sb.WS)
		}
		sys := agent.SystemPrompt(sb.WS, "default", append(
			harness.LayersFromDir(composeDir, limit),
			harness.Layer{Name: "probe-note", Content: probeNote(sb.WS)}))
		rec, _ := probe.Run(ctx, probe.Options{
			Name: "probe-successor", Cfg: opts.Cfg, ModelID: opts.ModelID, Model: opts.Model,
			System: sys, Task: task, Scratch: sb,
			OutDir:        filepath.Join(dir, "successor"),
			MaxIterations: opts.ProbeIterations, WallTimeoutS: opts.WallTimeoutS, Emit: emit,
		})
		res.SuccessorProbe = rec
	}

	emit(ev("improve.done", runid, map[string]any{"ok": true, "error": "",
		"decisions": clip(res.Decisions, 800), "successor": true}))
	return res, writeStep(dir, res)
}

// ---- stage agents ----

func editorTools(ws string) []dive.Tool {
	return []dive.Tool{
		toolkit.NewReadFileTool(toolkit.ReadFileToolOptions{WorkspaceDir: ws}),
		toolkit.NewGlobTool(toolkit.GlobToolOptions{WorkspaceDir: ws}),
		toolkit.NewGrepTool(toolkit.GrepToolOptions{WorkspaceDir: ws}),
		toolkit.NewWriteFileTool(toolkit.WriteFileToolOptions{WorkspaceDir: ws}),
		toolkit.NewEditTool(toolkit.EditToolOptions{WorkspaceDir: ws}),
	}
}

func runEditor(ctx context.Context, opts Options, kind, candDir, task string) error {
	a, err := dive.NewAgent(dive.AgentOptions{
		Name: "improve-editor-" + kind, SystemPrompt: editorSystem(kind),
		Model: opts.Model(), Tools: editorTools(candDir), ToolIterationLimit: opts.EditorIterations,
	})
	if err != nil {
		return err
	}
	_, err = a.CreateResponse(ctx, dive.WithInput(task))
	return err
}

func runComposer(ctx context.Context, opts Options, composeDir, task string) error {
	a, err := dive.NewAgent(dive.AgentOptions{
		Name: "improve-composer", SystemPrompt: composerSystem,
		Model: opts.Model(), Tools: editorTools(composeDir), ToolIterationLimit: opts.EditorIterations + 6,
	})
	if err != nil {
		return err
	}
	_, err = a.CreateResponse(ctx, dive.WithInput(task))
	return err
}

func generate(ctx context.Context, opts Options, system, task string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ { // one retry: small-model stream flukes
		a, err := dive.NewAgent(dive.AgentOptions{
			Name: "improve-note", SystemPrompt: system, Model: opts.Model(), ToolIterationLimit: 1,
		})
		if err != nil {
			return "", err
		}
		resp, err := a.CreateResponse(ctx, dive.WithInput(task))
		if err != nil {
			lastErr = err
			continue
		}
		return strings.TrimSpace(resp.OutputText()), nil
	}
	return "", lastErr
}

// ---- files & records ----

func probeNote(ws string) string {
	return "This is a fresh replay of one task inside an isolated copy of the workspace at " + ws +
		". Use absolute paths under it only; set bash working_directory inside it. " +
		"The replay exists to observe how these harness instructions change your behavior."
}

func ev(typ, run string, extra map[string]any) map[string]any {
	m := map[string]any{"type": typ, "run": run}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeLayers(dir string, layers []harness.Layer) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, l := range layers {
		if err := os.WriteFile(filepath.Join(dir, l.Name+".md"), []byte(l.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func layerContents(layers []harness.Layer) []probe.LayerContent {
	out := make([]probe.LayerContent, 0, len(layers))
	for _, l := range layers {
		out = append(out, probe.LayerContent{Name: l.Name, Content: l.Content})
	}
	return out
}

func copyFiles(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

// dirDiff renders changed/added/removed .md files between two dirs.
func dirDiff(a, b string, max int) string {
	files := map[string]bool{}
	for _, d := range []string{a, b} {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				files[e.Name()] = true
			}
		}
	}
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sortStrings(names)
	var sb strings.Builder
	total := 0
	for _, n := range names {
		ca, cb := readFile(filepath.Join(a, n)), readFile(filepath.Join(b, n))
		if ca == cb {
			continue
		}
		block := fmt.Sprintf("### %s\n%s\n", n, cb)
		if cb == "" {
			block = fmt.Sprintf("### %s (removed)\n", n)
		}
		if total+len(block) > max {
			sb.WriteString("… [diff truncated]\n")
			break
		}
		sb.WriteString(block)
		total += len(block)
	}
	return sb.String()
}

func changedFiles(a, b string) []string {
	var out []string
	for _, n := range mdFiles(b) {
		if readFile(filepath.Join(a, n)) != readFile(filepath.Join(b, n)) {
			out = append(out, n)
		}
	}
	return out
}

func mdFiles(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, e.Name())
		}
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func writeStep(dir string, res *Result) error {
	res.FinishedAt = time.Now().UTC()
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "step.json"), b, 0o644)
}
