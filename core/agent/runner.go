// Package agent composes and runs the dive agent for anneal.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/events"
	"github.com/uidbz/anneal/core/fsutil"
	"github.com/uidbz/anneal/core/harness"
	"github.com/uidbz/anneal/core/memory"
	"github.com/uidbz/anneal/core/model"
	"github.com/uidbz/anneal/core/paths"
	"github.com/uidbz/anneal/core/review"
	"github.com/uidbz/anneal/core/skills"
	"github.com/uidbz/anneal/core/textutil"
	"github.com/uidbz/anneal/core/tieindex"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/experimental/compaction"
	"github.com/deepnoodle-ai/dive/experimental/mcp"
	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/permission"
	"github.com/deepnoodle-ai/dive/session"
	"github.com/deepnoodle-ai/dive/toolkit"
)

func nowUTC() time.Time { return time.Now().UTC() }

// Runner owns the conversation session and executes one task turn at a time.
type Runner struct {
	cfg     *config.Config
	monitor *model.Monitor
	bus     *events.Bus
	cwd     string
	store   *session.FileStore
	mgr     *permission.Manager
	index   *tieindex.Indexer
	mem     *memory.Store
	skills  *skills.Store
	harn    *harness.Store
	review  *review.Manager
	mcp     *mcp.Manager
	lastAct time.Time

	mu           sync.Mutex
	sess         *session.Session
	sessID       string
	lastTask     string
	todos        []todoItem
	cancel       context.CancelFunc
	runSeq       int
	running      bool
	permSeq      int
	pendingPerms map[string]chan *dive.DialogOutput

	mcpMu sync.Mutex // guards mcp: it is attached asynchronously at boot
}

// SetIndex attaches the tie index (nil or disabled = no session_search).
func (r *Runner) SetIndex(idx *tieindex.Indexer) { r.index = idx }

// SetMemory attaches the memory store (nil or disabled = no memory tool).
func (r *Runner) SetMemory(m *memory.Store) { r.mem = m }

// SetSkills attaches the skill store (nil or disabled = no skill_manage).
func (r *Runner) SetSkills(s *skills.Store) { r.skills = s }

// HarnessStatus is the UI/CLI view of the editable harness (typed so the
// formatter cannot panic on a changed map shape).
type HarnessStatus struct {
	Root       string             `json:"root"`
	Enabled    bool               `json:"enabled"`
	Components []HarnessComponent `json:"components"`
	Rules      HarnessRules       `json:"rules"`
	Warnings   []string           `json:"warnings"`
}

type HarnessComponent struct {
	Name  string `json:"name"`
	Chars int    `json:"chars"`
}

type HarnessRules struct {
	Allow []string `json:"allow"`
	Ask   []string `json:"ask"`
	Deny  []string `json:"deny"`
}

// HarnessInfo reports the active harness components and warnings (UI/CLI).
func (r *Runner) HarnessInfo() HarnessStatus {
	out := HarnessStatus{
		Enabled:    r.cfg.Harness.Enabled,
		Components: []HarnessComponent{},
		Rules:      HarnessRules{Allow: []string{}, Ask: []string{}, Deny: []string{}},
		Warnings:   []string{},
	}
	if r.harn == nil {
		return out
	}
	out.Root = r.harn.Root()
	lim := r.cfg.Harness.CharLimit
	for _, l := range r.harn.Layers(lim) {
		out.Components = append(out.Components, HarnessComponent{Name: l.Name, Chars: len(l.Content)})
	}
	allow, ask, deny := r.harn.GuardRules()
	out.Rules = HarnessRules{Allow: nzs(allow), Ask: nzs(ask), Deny: nzs(deny)}
	out.Warnings = nzs(r.harn.Validate(lim))
	return out
}

func nzs(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// FormatHarnessInfo renders HarnessInfo for terminals.
func FormatHarnessInfo(info HarnessStatus) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "harness %q (enabled=%v)\n", info.Root, info.Enabled)
	for _, w := range info.Warnings {
		fmt.Fprintf(&sb, "  warn: %s\n", w)
	}
	for _, c := range info.Components {
		fmt.Fprintf(&sb, "  component %s: %d chars\n", c.Name, c.Chars)
	}
	fmt.Fprintf(&sb, "  rules: allow=%d ask=%d deny=%d",
		len(info.Rules.Allow), len(info.Rules.Ask), len(info.Rules.Deny))
	return sb.String()
}

// SessionID / LastTask feed the improvement step the reference execution
// identity (task text + final answer of last run).
func (r *Runner) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessID
}

func (r *Runner) LastTask() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastTask
}

// Cwd is the working directory runs operate in (improve sandbox source).
func (r *Runner) Cwd() string { return r.cwd }

// SetSessionOverlay installs an improvement successor harness (a directory
// of component .md files) as the current session's harness overlay: mounted
// on every following run of this session, nothing global is touched.
func (r *Runner) SetSessionOverlay(srcDir string) error {
	sid := r.SessionID()
	dst := filepath.Join(paths.Overlays(), sid)
	if err := fsutil.CopyFlatDir(srcDir, dst, nil); err != nil {
		return err
	}
	r.emit(map[string]any{"type": "harness.overlay", "session": sid, "dir": dst})
	return nil
}

func (r *Runner) sessionOverlayLayers() []harness.Layer {
	ovl := harness.LayersFromDir(filepath.Join(paths.Overlays(), r.SessionID()), r.cfg.Harness.CharLimit)
	for i := range ovl {
		ovl[i].Name = "overlay:" + ovl[i].Name
	}
	return ovl
}

func copyTree(src, dst string) error {
	return fsutil.CopyFlatDir(src, dst, nil)
}

// SetReview attaches the background review manager (nil = no loop).
func (r *Runner) SetReview(m *review.Manager) { r.review = m }

func NewRunner(cfg *config.Config, monitor *model.Monitor, bus *events.Bus, cwd string, harn *harness.Store) (*Runner, error) {
	if harn == nil {
		harn = harness.New(paths.Harness())
	}
	r := &Runner{
		cfg:          cfg,
		monitor:      monitor,
		bus:          bus,
		cwd:          cwd,
		harn:         harn,
		pendingPerms: make(map[string]chan *dive.DialogOutput),
	}
	r.mgr = permission.NewManager(permConfig(cfg, r.harn), &uiDialog{r: r})
	if err := r.initStore(); err != nil {
		return nil, err
	}
	return r, nil
}

// ReplayState emits the current session transcript, permission mode, and todos.
// UI frontends call it after attaching their event consumer: the runner is
// constructed (and the session resumed) before any tap/script handler exists.
func (r *Runner) ReplayState() {
	r.mu.Lock()
	sess, id := r.sess, r.sessID
	todos := append([]todoItem{}, r.todos...)
	r.mu.Unlock()
	blocks := transcriptBlocks(context.Background(), sess)
	r.emit(map[string]any{"type": "session.opened", "id": id, "title": sess.Title(), "blocks": blocks})
	r.emit(map[string]any{"type": "mode.changed", "mode": r.Mode()})
	if len(todos) > 0 {
		r.emit(map[string]any{"type": "todo.updated", "items": todos})
	}
}

// Send starts a run; at most one run is active.
func (r *Runner) Send(input string) {
	r.StartTask(input)
}

// StartTask starts a run and returns its run id. ok is false when a run is
// already active (a run.busy event is still emitted). Callers that must
// correlate events with the run they started use this instead of Send.
func (r *Runner) StartTask(input string) (id int, ok bool) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		r.bus.Emit(map[string]any{"type": "run.busy"})
		return 0, false
	}
	r.running = true
	r.runSeq++
	r.lastTask = input
	r.lastAct = time.Now()
	id = r.runSeq
	r.mu.Unlock()
	if r.review != nil {
		r.review.Interrupt() // foreground preempts background
	}
	go r.run(id, input)
	return id, true
}

// Busy reports whether a foreground run is active.
func (r *Runner) Busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// LastActivity is when the most recent run ended (idle scheduler input).
func (r *Runner) LastActivity() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastAct
}

// Interrupt soft-cancels the active run (running tool calls finish first).
func (r *Runner) Interrupt() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *Runner) run(id int, input string) {
	defer r.clearRunning() // safety net for early returns / panics

	h := r.monitor.Current()
	if h.State == model.StateConnecting {
		r.monitor.ProbeNow() // first-send race: probe synchronously once
		h = r.monitor.Current()
	}
	if !h.IsUsable() {
		r.finishRun(id, false, "", fmt.Sprintf("model server not usable (state: %s)", h.State), 0, 0, 0)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()

	deltas := newDeltaBatcher(r.bus, id)
	defer deltas.close()

	provider := model.NewProvider(r.cfg.Provider, h.Model)

	r.mu.Lock()
	sess := r.sess
	r.mu.Unlock()
	if sess.Title() == "" {
		sess.SetTitle(clip(input, 60))
	}

	tools := []dive.Tool{
		toolkit.NewReadFileTool(),
		toolkit.NewWriteFileTool(),
		toolkit.NewEditTool(),
		toolkit.NewTextEditorTool(),
		toolkit.NewGlobTool(),
		toolkit.NewGrepTool(),
		toolkit.NewListDirectoryTool(),
		toolkit.NewBashTool(),
		toolkit.NewFetchTool(),
		r.todoTool(),
	}
	if r.index != nil && r.index.Enabled() {
		tools = append(tools, r.sessionSearchTool())
	}
	if r.mem != nil && r.cfg.Memory.Enabled {
		tools = append(tools, r.memoryTool())
	}
	if r.skills != nil && r.cfg.Skills.Enabled {
		tools = append(tools, r.skillManageTool())
	}
	if r.cfg.Agent.Script {
		tools = append(tools, r.scriptTool())
	}
	skillExt := r.skillLoader()
	var exts []dive.Extension
	if skillExt != nil {
		exts = append(exts, skillExt)
	}
	tools = append(tools, mcpTools(r.mcpManager())...)
	if r.cfg.Agent.Subagents {
		tools = append(tools, r.agentTool(provider, tools))
	}

	var layers []harness.Layer
	if r.harn != nil && r.cfg.Harness.Enabled {
		layers = r.harn.Layers(r.cfg.Harness.CharLimit)
		layers = append(layers, r.sessionOverlayLayers()...)
	}
	prompt := SystemPrompt(r.cwd, r.Mode(), layers)
	hooks := dive.Hooks{
		PreToolUse: []dive.PreToolUseHook{permission.HookFromManager(r.mgr)},
	}
	hooks.PreGeneration = []dive.PreGenerationHook{r.memoryReminderHook()}
	if r.cfg.Compaction.Enabled {
		threshold := compactionThreshold(h.CtxWindow, r.cfg.Compaction.ThresholdPct)
		hooks.PreGeneration = append(hooks.PreGeneration, r.preGenCompactionHook(provider, prompt, threshold))
		hooks.PreIteration = []dive.PreIterationHook{
			compaction.MidTurnCompactionHook(provider, threshold,
				compaction.WithMidTurnSystemPrompt(prompt),
				compaction.WithMidTurnNotify(r.notifyCompaction)),
		}
	}

	ag, err := dive.NewAgent(dive.AgentOptions{
		Name:               "anneal",
		SystemPrompt:       prompt,
		Model:              provider,
		Tools:              tools,
		Hooks:              hooks,
		Extensions:         exts,
		ToolIterationLimit: r.cfg.Agent.ToolIterationLimit,
		Session:            sess,
		ModelSettings:      model.Settings(r.cfg),
	})
	if err != nil {
		r.finishRun(id, false, "", err.Error(), 0, 0, 0)
		return
	}

	r.emit(map[string]any{"type": "run.started", "run": id, "model": h.Model})

	resp, runErr := ag.CreateResponse(ctx,
		dive.WithInput(input),
		dive.WithEventCallback(func(_ context.Context, item *dive.ResponseItem) error {
			switch item.Type {
			case dive.ResponseItemTypeModelEvent:
				if item.Event != nil && item.Event.Delta != nil {
					if item.Event.Delta.Text != "" {
						deltas.addText(item.Event.Delta.Text)
					}
					if item.Event.Delta.Thinking != "" {
						r.emit(map[string]any{"type": "thinking.delta", "run": id, "text": item.Event.Delta.Thinking})
					}
				}
			case dive.ResponseItemTypeMessage:
				deltas.flush()
				if item.Message != nil {
					r.emit(map[string]any{"type": "message.final", "run": id, "text": item.Message.AnswerText()})
				}
			case dive.ResponseItemTypeToolCall:
				deltas.flush()
				if item.ToolCall != nil {
					r.emit(map[string]any{"type": "tool.start", "run": id, "id": item.ToolCall.ID,
						"name": item.ToolCall.Name, "input": previewJSON(item.ToolCall.Input, 400)})
				}
			case dive.ResponseItemTypeToolCallResult:
				if item.ToolCallResult != nil {
					tr := item.ToolCallResult
					text := ""
					isErr := tr.Error != nil || (tr.Result != nil && tr.Result.IsError)
					if tr.Error != nil {
						text = tr.Error.Error()
					} else if tr.Result != nil {
						text = toolResultText(tr.Result)
					}
					r.emit(map[string]any{"type": "tool.result", "run": id, "id": tr.ID,
						"name": tr.Name, "error": isErr, "preview": clip(text, 600)})
				}
			}
			return nil
		}),
	)
	deltas.flush()

	out := map[string]any{"type": "run.finished", "run": id, "ok": runErr == nil,
		"text": "", "error": "", "usage": map[string]any{"tin": 0, "tout": 0}, "ctx_pct": 0}
	if runErr != nil {
		out["error"] = runErr.Error()
	}
	if resp != nil {
		out["text"] = clip(resp.OutputText(), 4000)
		if resp.Usage != nil {
			used := resp.Usage.InputTokens + resp.Usage.OutputTokens
			out["usage"] = map[string]any{"tin": resp.Usage.InputTokens, "tout": resp.Usage.OutputTokens}
			if h.CtxWindow > 0 {
				out["ctx_pct"] = 100 * used / h.CtxWindow
			}
		}
	}
	// Persist session-scoped state (title, todo list) — event appends
	// don't rewrite the header.
	r.mu.Lock()
	items := append([]todoItem{}, r.todos...)
	r.mu.Unlock()
	// Always persist: an empty list must overwrite a stale one.
	sess.SetMetadata("todos", items)
	_ = r.store.Put(context.Background(), sess)
	r.indexSessionNow(sess, h.Model)
	if r.review != nil {
		r.review.Enqueue(sess.ID())
	}
	// Clear before announcing completion: a UI that chains sends on
	// run.finished must not hit run.busy.
	r.clearRunning()
	r.emit(out)
}

// clearRunning marks the runner idle (always before run.finished emits).
func (r *Runner) clearRunning() {
	r.mu.Lock()
	r.running, r.cancel = false, nil
	r.lastAct = time.Now()
	r.mu.Unlock()
}

// compactionThreshold maps ctx window + config percentage to a token budget
// (dive estimates tokens as ~bytes/4, so this is intentionally approximate).
func compactionThreshold(ctxWindow, pct int) int {
	if ctxWindow <= 0 {
		return compaction.DefaultContextTokenThreshold
	}
	if pct <= 0 || pct > 95 {
		pct = 82
	}
	t := ctxWindow * pct / 100
	if t < 8000 {
		t = 8000
	}
	return t
}

func (r *Runner) notifyCompaction(ev *compaction.CompactionEvent) {
	r.emit(map[string]any{"type": "compaction.done",
		"before": ev.TokensBefore, "after": ev.TokensAfter, "msgs": ev.MessagesCompacted})
}

// preGenCompactionHook compacts persisted between-turn history when the
// (estimated) context exceeds threshold; keeps the full transcript readable
// via AllMessages, the summary is what the model sees next.
func (r *Runner) preGenCompactionHook(provider llm.LLM, prompt string, threshold int) dive.PreGenerationHook {
	return func(ctx context.Context, hctx *dive.HookContext) error {
		est := 0
		for _, m := range hctx.Messages {
			est += len(m.Text()) / 4
		}
		if est < threshold {
			return nil
		}
		compacted, ev, err := compaction.CompactMessages(ctx, provider, hctx.Messages, prompt, "", est)
		if err != nil {
			return nil // best effort
		}
		hctx.Messages = compacted
		if hctx.Values != nil {
			hctx.Values[dive.StateKeyCompactionEvent] = ev
		}
		r.notifyCompaction(ev)
		return nil
	}
}

func (r *Runner) finishRun(id int, ok bool, text, errMsg string, in, outTok, ctxPct int) {
	r.clearRunning()
	r.emit(map[string]any{"type": "run.finished", "run": id, "ok": ok,
		"text": text, "error": errMsg,
		"usage": map[string]any{"tin": in, "tout": outTok}, "ctx_pct": ctxPct})
}

func (r *Runner) emit(v any) { r.bus.Emit(v) }

func toolResultText(tr *dive.ToolResult) string {
	var sb strings.Builder
	for _, c := range tr.Content {
		if c.Text != "" {
			sb.WriteString(c.Text)
		}
	}
	if sb.Len() == 0 && tr.Display != "" {
		return tr.Display
	}
	return sb.String()
}

func previewJSON(v any, max int) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return clip(string(b), max)
}

func clip(s string, max int) string {
	return textutil.Clip(s, max)
}

// deltaBatcher coalesces text deltas so the script handler sees ≤ ~20/s.
type deltaBatcher struct {
	bus     *events.Bus
	run     int
	mu      sync.Mutex
	buf     string
	last    time.Time
	pending *time.Timer
}

func newDeltaBatcher(b *events.Bus, run int) *deltaBatcher {
	return &deltaBatcher{bus: b, run: run}
}

const (
	batchChars = 240
	batchIdle  = 60 * time.Millisecond
)

func (d *deltaBatcher) addText(s string) {
	d.mu.Lock()
	d.buf += s
	if len(d.buf) >= batchChars {
		d.flushLocked()
	} else if d.pending == nil {
		d.pending = time.AfterFunc(batchIdle, func() {
			d.mu.Lock()
			d.flushLocked()
			d.mu.Unlock()
		})
	}
	d.mu.Unlock()
}

func (d *deltaBatcher) flush() {
	d.mu.Lock()
	d.flushLocked()
	d.mu.Unlock()
}

func (d *deltaBatcher) flushLocked() {
	if d.pending != nil {
		d.pending.Stop()
		d.pending = nil
	}
	if d.buf == "" {
		return
	}
	d.bus.Emit(map[string]any{"type": "text.delta", "run": d.run, "text": d.buf})
	d.buf = ""
}

func (d *deltaBatcher) close() {
	d.flush()
}
