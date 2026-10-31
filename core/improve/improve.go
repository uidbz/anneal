// Package improve implements anneal's task-level recursion: the contrastive
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
	"sort"
	"strings"
	"time"

	"github.com/uidbz/anneal/core/agent"
	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/fsutil"
	"github.com/uidbz/anneal/core/harness"
	"github.com/uidbz/anneal/core/harnesslib"
	"github.com/uidbz/anneal/core/paths"
	"github.com/uidbz/anneal/core/probe"
	"github.com/uidbz/anneal/core/textutil"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/toolkit"
)

// editorKinds are the two scoped Propose roles.
var editorKinds = [...]string{"invariant", "domain"}

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

	MaxDigestChars   int      // default 12000
	ProbeIterations  int      // default 25
	EditorIterations int      // default 12
	WallTimeoutS     int      // per probe, default 300
	RunID            string   // override the timestamp run id (tests)
	K                int      // recursion depth (default 1)
	Mode             string   // "sequential" (default) | "parallel"
	TotalWallS       int      // whole-step wall budget, seconds (0 = none)
	MaxTotalTokens   int      // stop before next iteration past this usage (0 = none)
	Resume           bool     // skip iterations whose step.json already exists (with RunID)
	Hints            []string // reviewer-flagged deficiencies; seed editor/composer prompts

	Library *harnesslib.Store     // improvement library (nil = disabled)
	IndexFn func(id, text string) // optional tie indexing of experience notes
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
	LibraryEntry   string        `json:"library_entry,omitempty"`
	Iter           int           `json:"iter,omitempty"`
	K              int           `json:"k,omitempty"`
	Mode           string        `json:"mode,omitempty"`
	Hints          []string      `json:"hints,omitempty"`
	Note           string        `json:"note,omitempty"`
	Iterations     []iterRef     `json:"iterations,omitempty"`
	// StageTokens counts editor/composer/contrast/reflection model usage.
	// Probe usage lives in the probe records; both feed the token governor.
	StageTokens int       `json:"stage_tokens,omitempty"`
	FinishedAt  time.Time `json:"finished_at"`
}

// iterRef summarizes one iteration of a k>1 recursion.
type iterRef struct {
	Iter      int    `json:"iter"`
	Dir       string `json:"dir"`
	Successor string `json:"successor,omitempty"`
	Entry     string `json:"entry,omitempty"`
	Skipped   string `json:"skipped,omitempty"`
}

// loadStep reads a completed iteration record for resume/chaining.
func loadStep(dir string) (*Result, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "step.json"))
	if err != nil {
		return nil, false
	}
	var r Result
	if json.Unmarshal(b, &r) != nil || r.RunID == "" {
		return nil, false
	}
	return &r, true
}

// LoadRuns lists improvement records under root (one directory per run,
// step.json inside), newest first. Corrupt or partial records are skipped.
// limit <= 0 means all.
func LoadRuns(root string, limit int) []Result {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	out := make([]Result, 0, len(names))
	for _, name := range names {
		r, ok := loadStep(filepath.Join(root, name))
		if !ok {
			continue
		}
		out = append(out, *r)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// tokens sums reported usage across an iteration's executions.
func tokens(r *Result) int {
	n := r.StageTokens
	for _, c := range r.Candidates {
		if c.Probe != nil {
			n += c.Probe.Tin + c.Probe.Tout
		}
	}
	if r.SuccessorProbe != nil {
		n += r.SuccessorProbe.Tin + r.SuccessorProbe.Tout
	}
	return n
}

func clip(s string, n int) string {
	return textutil.Clip(s, n)
}

// Step runs one contrastive improvement step (k=1), or a budgeted
// recursion when K>1: sequential chaining (each iteration's probed
// successor becomes the next reference) or parallel candidates plus an
// anonymized deliverable selector. Every stage is recorded; the returned
// Result is complete even when the step bails early.
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
	if opts.Emit == nil {
		opts.Emit = func(any) {}
	}
	if opts.TotalWallS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(opts.TotalWallS)*time.Second)
		defer cancel()
	}
	k := opts.K
	if k < 1 {
		k = 1
	}
	mode := strings.ToLower(opts.Mode)
	if mode == "" {
		mode = "sequential"
	}
	emit := opts.Emit
	limit := opts.Cfg.Harness.CharLimit

	runid := opts.RunID
	if runid == "" {
		runid = time.Now().UTC().Format("20060102-150405")
	}
	dir := filepath.Join(paths.Improvements(), runid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	// library experience re-enters refinement (selective re-draft, never
	// wholesale inheritance)
	libCtx := ""
	if opts.Library != nil {
		libCtx = opts.Library.Context(opts.Task, 3, 3000)
	}
	if len(opts.Hints) > 0 {
		libCtx += hintsBlock(opts.Hints)
	}

	var baseLayers []harness.Layer
	if opts.Harness != nil && opts.Cfg.Harness.Enabled {
		baseLayers = opts.Harness.Layers(limit)
	}
	h0hash := probe.HarnessHash(layerContents(baseLayers))

	// fast path: single step (unchanged M12 shape: everything at dir/)
	if k == 1 && mode != "parallel" {
		emit(ev("improve.start", runid, map[string]any{"session": opts.SessionID, "harness_hash": h0hash}))
		res, err := stepOnce(ctx, &opts, dir, runid, 1, baseLayers, opts.RefDigest, libCtx)
		if err != nil {
			return res, err
		}
		res.K, res.Mode = 1, mode
		ok := res.Successor != "" || res.Decisions != ""
		errMsg := ""
		if !ok {
			errMsg = "no candidate produced probed edits"
		}
		emit(ev("improve.done", runid, map[string]any{"ok": ok, "error": errMsg,
			"decisions": clip(res.Decisions, 800), "successor": res.Successor != "",
			"k": 1, "mode": mode}))
		return res, writeStep(dir, res)
	}
	return stepK(ctx, &opts, dir, runid, k, mode, baseLayers, h0hash, libCtx)
}

// stepK runs k iterations under dir/iter<i> per the locked plan default
// (sequential), with token/wall governors and per-iteration resume.
func stepK(ctx context.Context, opts *Options, dir, runid string, k int, mode string,
	baseLayers []harness.Layer, h0hash, libCtx string) (*Result, error) {
	emit := opts.Emit
	limit := opts.Cfg.Harness.CharLimit
	res := &Result{RunID: runid, Dir: dir, SessionID: opts.SessionID, Task: opts.Task,
		K: k, Mode: mode, HarnessHash: h0hash, Hints: opts.Hints}
	emit(ev("improve.start", runid, map[string]any{"session": opts.SessionID,
		"harness_hash": h0hash, "k": k, "mode": mode}))

	var sub []*Result
	curLayers, refd := baseLayers, opts.RefDigest
	spent, stopped := 0, ""
	for i := 1; i <= k; i++ {
		iterDir := filepath.Join(dir, fmt.Sprintf("iter%d", i))
		if opts.Resume {
			if prev, ok := loadStep(iterDir); ok {
				sub = append(sub, prev)
				res.Iterations = append(res.Iterations, iterRef{Iter: i, Dir: iterDir,
					Successor: prev.Successor, Entry: prev.LibraryEntry, Skipped: "resumed"})
				spent += tokens(prev)
				if mode == "sequential" {
					if prev.Successor == "" {
						stopped = fmt.Sprintf("iter %d kept the harness", i)
						break
					}
					curLayers = harness.LayersFromDir(prev.Successor, limit)
					if prev.SuccessorProbe != nil {
						refd = prev.SuccessorProbe.Digest(opts.MaxDigestChars)
					}
				}
				continue
			}
		}
		if opts.MaxTotalTokens > 0 && spent >= opts.MaxTotalTokens {
			emit(ev("improve.budget", runid, map[string]any{"spent_tokens": spent,
				"budget": opts.MaxTotalTokens, "iter": i}))
			stopped = fmt.Sprintf("token budget reached after iter %d", i-1)
			break
		}
		ir, err := stepOnce(ctx, opts, iterDir, runid, i, curLayers, refd, libCtx)
		if err != nil {
			return res, err
		}
		ir.K, ir.Mode = k, mode
		sub = append(sub, ir)
		res.Iterations = append(res.Iterations, iterRef{Iter: i, Dir: iterDir,
			Successor: ir.Successor, Entry: ir.LibraryEntry})
		spent += tokens(ir)
		if mode == "sequential" {
			if ir.Successor == "" {
				stopped = fmt.Sprintf("iter %d kept the harness", i)
				break
			}
			curLayers = harness.LayersFromDir(ir.Successor, limit)
			if ir.SuccessorProbe != nil {
				refd = ir.SuccessorProbe.Digest(opts.MaxDigestChars)
			}
		}
	}
	if len(sub) == 0 {
		return res, writeStep(dir, res)
	}

	final := sub[len(sub)-1]
	if mode == "parallel" {
		final = selectSuccessor(ctx, opts, runid, sub)
	}
	res.Iter = final.Iter
	res.Candidates = final.Candidates
	res.Decisions = final.Decisions
	res.Successor = final.Successor
	res.SuccessorProbe = final.SuccessorProbe
	res.LibraryEntry = final.LibraryEntry
	res.Note = stopped
	emit(ev("improve.done", runid, map[string]any{"ok": true, "error": "",
		"decisions": clip(res.Decisions, 800), "successor": res.Successor != "",
		"k": k, "mode": mode, "iters_done": len(sub)}))
	return res, writeStep(dir, res)
}

// selectSuccessor compares anonymized parallel candidates by their final
// deliverables (paper §2.2.2 k>1 selection); the iteration identity is
// hidden from the selector on purpose.
func selectSuccessor(ctx context.Context, opts *Options, runid string, sub []*Result) *Result {
	emit := opts.Emit
	var elig []*Result
	for _, r := range sub {
		if r != nil && r.Successor != "" && r.SuccessorProbe != nil {
			elig = append(elig, r)
		}
	}
	if len(elig) == 0 {
		return sub[len(sub)-1]
	}
	if len(elig) == 1 {
		emit(ev("improve.selector", runid, map[string]any{"chosen_iter": elig[0].Iter,
			"label": "only", "reason": "single probed successor"}))
		return elig[0]
	}
	labels := []string{"A", "B", "C", "D", "E", "F", "G", "H"}
	var blocks []string
	for i, r := range elig {
		li := labels[i%len(labels)]
		blocks = append(blocks, fmt.Sprintf("OPTION %s\nfinal run:\n%s\ndecisions:\n%s\n",
			li, r.SuccessorProbe.Digest(2500), clip(r.Decisions, 1200)))
	}
	out, _, _, err := generate(ctx, *opts,
		"You compare anonymized candidate outcomes for one task. Choose by task fit and observed behavior, never by length, confidence, or ordering. Output one letter or NONE.",
		selectorTask(opts.Task, opts.RefDigest, strings.Join(blocks, "\n")))
	pick := ""
	if err == nil {
		for _, r := range strings.ToUpper(strings.TrimSpace(out)) {
			if r >= 'A' && r <= 'H' {
				pick = string(r)
				break
			}
		}
	}
	// Default when the selector declines: keep the LAST iteration (matching
	// the recorded reason; earlier iterations are superseded in spirit).
	idx := len(elig) - 1
	reason := "none clearly better (kept last)"
	for i := range elig {
		if pick != "" && labels[i%len(labels)] == pick {
			idx, reason = i, "selector picked "+pick
			break
		}
	}
	emit(ev("improve.selector", runid, map[string]any{"chosen_iter": elig[idx].Iter,
		"label": pick, "reason": reason}))
	return elig[idx]
}

// stepOnce runs the Propose→Probe→Compose→Reflect pipeline inside dir,
// starting from layers0 as the base harness and refd as the reference
// execution digest. Records (candidates/, probes/, compose/, step.json)
// live under dir.
func stepOnce(ctx context.Context, opts *Options, dir, runid string, iter int,
	layers0 []harness.Layer, refd, libCtx string) (*Result, error) {
	emit := opts.Emit
	limit := opts.Cfg.Harness.CharLimit
	res := &Result{RunID: runid, Dir: dir, SessionID: opts.SessionID, Task: opts.Task, Iter: iter, Hints: opts.Hints}

	// ℋ snapshot as component files (the base every candidate starts from)
	h0 := filepath.Join(dir, "harness0")
	res.HarnessHash = probe.HarnessHash(layerContents(layers0))
	if err := writeLayers(h0, layers0); err != nil {
		return res, err
	}

	// Propose: two scoped editors, one sandboxed candidate dir each.
	for _, kind := range editorKinds {
		c := Candidate{Kind: kind, Dir: filepath.Join(dir, "candidates", kind)}
		emit(ev("improve.propose", runid, map[string]any{"kind": kind, "iter": iter}))
		if err := copyFiles(h0, c.Dir); err != nil {
			c.Skipped = err.Error()
			res.Candidates = append(res.Candidates, c)
			continue
		}
		tin, tout, err := runEditor(ctx, *opts, kind, c.Dir, editorTask(opts.Task, refd, c.Dir, libCtx))
		res.StageTokens += tin + tout
		if err != nil {
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
		candLayers := harness.LayersFromDir(c.Dir, limit)
		sys := agent.SystemPrompt(sb.WS, "default", append(candLayers,
			harness.Layer{Name: "probe-note", Content: probeNote(sb.WS)}))
		rec, runErr := probe.Run(ctx, probe.Options{
			Name: "probe-" + c.Kind, Cfg: opts.Cfg, ModelID: opts.ModelID, Model: opts.Model,
			System: sys, Task: task, Scratch: sb,
			HarnessHash:   probe.HarnessHash(layerContents(candLayers)),
			OutDir:        filepath.Join(dir, "probes"),
			MaxIterations: opts.ProbeIterations, WallTimeoutS: opts.WallTimeoutS, Emit: emit,
		})
		c.Probe = rec
		emit(ev("improve.probe", runid, map[string]any{"kind": c.Kind, "iter": iter,
			"ok": runErr == nil, "steps": len(rec.Steps), "duration_s": int(rec.DurationS),
			"error": errStr(runErr)}))
		note, tin, tout, err := generate(ctx, *opts, "You write terse, evidence-quoted behavioral contrast notes. No scores.",
			contrastPrompt(c.Kind, opts.Task, refd, c.Diff, rec.Digest(opts.MaxDigestChars)))
		res.StageTokens += tin + tout
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
		emit(ev("improve.iterdone", runid, map[string]any{"iter": iter, "ok": false,
			"error": "no candidate produced probed edits", "successor": false}))
		return res, writeStep(dir, res)
	}
	composeDir := filepath.Join(dir, "compose")
	if err := copyFiles(h0, composeDir); err != nil {
		return res, writeStep(dir, res)
	}
	tin, tout, composerErr := runComposer(ctx, *opts, composeDir,
		composerTask(opts.Task, refd, strings.Join(blocks, "\n"), composeDir, libCtx))
	res.StageTokens += tin + tout
	if composerErr != nil {
		emit(ev("improve.iterdone", runid, map[string]any{"iter": iter, "ok": false,
			"error": "composer: " + composerErr.Error(), "successor": false}))
		return res, writeStep(dir, res)
	}
	res.Decisions = readFile(filepath.Join(composeDir, "decisions.md"))
	changed := strings.TrimSpace(dirDiff(h0, composeDir, 60000))
	if changed == "" {
		emit(ev("improve.iterdone", runid, map[string]any{"iter": iter, "ok": true, "error": "",
			"successor": false, "note": "composer kept the current harness"}))
		return res, writeStep(dir, res)
	}
	res.Successor = composeDir
	emit(ev("improve.compose", runid, map[string]any{"changed": changedFiles(h0, composeDir), "iter": iter}))

	// Successor execution e′ (evidence for the library + next reference).
	sb, err := probe.Prepare(opts.Workdir, filepath.Join(dir, "successor"))
	if err == nil {
		task := opts.Task
		if opts.Workdir != "" {
			task = strings.ReplaceAll(task, opts.Workdir, sb.WS)
		}
		succLayers := harness.LayersFromDir(composeDir, limit)
		sys := agent.SystemPrompt(sb.WS, "default", append(succLayers,
			harness.Layer{Name: "probe-note", Content: probeNote(sb.WS)}))
		rec, _ := probe.Run(ctx, probe.Options{
			Name: "probe-successor", Cfg: opts.Cfg, ModelID: opts.ModelID, Model: opts.Model,
			System: sys, Task: task, Scratch: sb,
			HarnessHash:   probe.HarnessHash(layerContents(succLayers)),
			OutDir:        filepath.Join(dir, "successor"),
			MaxIterations: opts.ProbeIterations, WallTimeoutS: opts.WallTimeoutS, Emit: emit,
		})
		res.SuccessorProbe = rec
	}

	// Harnessing-level recursion: reflect and record — ONLY with
	// execution-supported evidence (paper rule: else no entry).
	if opts.Library != nil {
		var contrasts strings.Builder
		for _, c := range res.Candidates {
			if c.Contrast != "" {
				fmt.Fprintf(&contrasts, "[%s] %s\n", c.Kind, clip(c.Contrast, 1200))
			}
		}
		succ := ""
		if res.SuccessorProbe != nil {
			succ = res.SuccessorProbe.Digest(3000)
		}
		note, tin, tout, err := generate(ctx, *opts,
			"You distill one harness improvement step into library experience notes. Terse; unsupported means NONE.",
			reflectorTask(opts.Task, res.Decisions, contrasts.String(), clip(refd, 2500), succ))
		res.StageTokens += tin + tout
		if err == nil && !strings.EqualFold(strings.TrimSpace(note), "NONE") &&
			len(strings.TrimSpace(note)) >= 60 && hasExecutionEvidence(res) {
			id, cerr := opts.Library.CreateEntry(composeDir, opts.Task, opts.SessionID, runid, res.HarnessHash, strings.TrimSpace(note))
			if cerr == nil {
				res.LibraryEntry = id
				emit(ev("improve.library", runid, map[string]any{"entry": id, "error": "", "iter": iter}))
				if opts.IndexFn != nil {
					go opts.IndexFn(id, note)
				}
			} else {
				emit(ev("improve.library", runid, map[string]any{"entry": "", "error": cerr.Error(), "iter": iter}))
			}
		}
	}

	emit(ev("improve.iterdone", runid, map[string]any{"iter": iter, "ok": true, "error": "",
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

func runEditor(ctx context.Context, opts Options, kind, candDir, task string) (int, int, error) {
	a, err := dive.NewAgent(dive.AgentOptions{
		Name: "improve-editor-" + kind, SystemPrompt: editorSystem(kind),
		Model: opts.Model(), Tools: editorTools(candDir), ToolIterationLimit: opts.EditorIterations,
	})
	if err != nil {
		return 0, 0, err
	}
	resp, err := a.CreateResponse(ctx, dive.WithInput(task))
	in, out := respUsage(resp)
	return in, out, err
}

func runComposer(ctx context.Context, opts Options, composeDir, task string) (int, int, error) {
	a, err := dive.NewAgent(dive.AgentOptions{
		Name: "improve-composer", SystemPrompt: composerSystem,
		Model: opts.Model(), Tools: editorTools(composeDir), ToolIterationLimit: opts.EditorIterations + 6,
	})
	if err != nil {
		return 0, 0, err
	}
	resp, err := a.CreateResponse(ctx, dive.WithInput(task))
	in, out := respUsage(resp)
	return in, out, err
}

func generate(ctx context.Context, opts Options, system, task string) (string, int, int, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ { // one retry: small-model stream flukes
		a, err := dive.NewAgent(dive.AgentOptions{
			Name: "improve-note", SystemPrompt: system, Model: opts.Model(), ToolIterationLimit: 1,
		})
		if err != nil {
			return "", 0, 0, err
		}
		resp, err := a.CreateResponse(ctx, dive.WithInput(task))
		if err != nil {
			lastErr = err
			continue
		}
		in, out := respUsage(resp)
		return strings.TrimSpace(resp.OutputText()), in, out, nil
	}
	return "", 0, 0, lastErr
}

func respUsage(resp *dive.Response) (int, int) {
	if resp == nil || resp.Usage == nil {
		return 0, 0
	}
	return resp.Usage.InputTokens, resp.Usage.OutputTokens
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
	return fsutil.CopyFlatDir(src, dst, nil)
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
	sort.Strings(names)
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
	seen := map[string]bool{}
	for _, d := range []string{a, b} {
		for _, n := range mdFiles(d) {
			seen[n] = true
		}
	}
	var out []string
	for n := range seen {
		if readFile(filepath.Join(a, n)) != readFile(filepath.Join(b, n)) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
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
	sort.Strings(out)
	return out
}

// hasExecutionEvidence is the library's admission rule: at least one replay
// must have actually executed a tool step. A successor probe that never
// started (wall/cancel) does not by itself justify an entry, but candidate
// probe evidence does.
func hasExecutionEvidence(res *Result) bool {
	if res.SuccessorProbe != nil && len(res.SuccessorProbe.Steps) > 0 {
		return true
	}
	for _, c := range res.Candidates {
		if c.Probe != nil && len(c.Probe.Steps) > 0 {
			return true
		}
	}
	return false
}

// writeStep persists the step record.
func writeStep(dir string, res *Result) error {
	res.FinishedAt = time.Now().UTC()
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "step.json"), b, 0o644)
}
