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
	"github.com/uidbz/anneal/core/permission"
	"github.com/uidbz/anneal/core/review"
	"github.com/uidbz/anneal/core/skills"
	"github.com/uidbz/anneal/core/textutil"
	"github.com/uidbz/anneal/core/tieindex"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/experimental/compaction"
	"github.com/deepnoodle-ai/dive/experimental/mcp"
	"github.com/deepnoodle-ai/dive/llm"
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
	perm    *permission.Manager
	index   *tieindex.Indexer
	mem     *memory.Store
	skills  *skills.Store
	harn    *harness.Store
	review  *review.Manager
	mcp     *mcp.Manager
	lastAct time.Time

	mu       sync.Mutex
	sess     *session.Session
	sessID   string
	lastTask string
	todos    []todoItem
	// Staleness: tool calls since the last todo set, and when that was.
	toolCalls     int
	todoCallsAt   int
	todoUpdatedAt time.Time
	todoWarned    bool

	cancel     context.CancelFunc
	softCancel func() // graceful stop: finish the running tool, start no new one
	stopping   bool
	runSeq     int
	running    bool
	permWait   time.Duration // time blocked on approval cards, per run
	lastPerf   *perfSummary
	preempt    func() // cancels background model work (review/improve)

	permMu       sync.Mutex // guards pendingPerms and doom counts
	permSeq      int
	pendingPerms map[string]*pendingPerm
	doom         map[string]int

	// Approval-churn telemetry (per session): enough prompts in one session
	// means the guardrails should change, not the user's patience.
	churnSession string
	churnCount   int
	churnPattern map[string]int
	churnFlagged map[string]bool

	mcpMu sync.Mutex // guards mcp: it is attached asynchronously at boot

	indexCh chan indexJob // background tie indexing (see search.go)

	sessMu      sync.Mutex
	sessCache   []SessionInfo
	sessCacheAt time.Time

	// gortex status: its tools only work in tracked repositories. When the
	// working directory is untracked they are omitted (saving prompt tokens
	// and failing calls) until the user tracks the project.
	gortexMu        sync.Mutex
	gortexChecked   bool
	gortexUntracked bool
	gortexTracked   []string

	// Input queue (see queue.go): messages sent while a run is active.
	queue       []QueuedInput
	queuePaused bool
	queueReason string
	inputSeq    int
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

// SetPreempt registers the callback that yields the model server: it is
// invoked when a foreground run starts and when the user starts typing.
func (r *Runner) SetPreempt(fn func()) {
	r.mu.Lock()
	r.preempt = fn
	r.mu.Unlock()
}

// Preempt asks background work (review, improve) to stand down now.
func (r *Runner) Preempt() {
	r.mu.Lock()
	fn := r.preempt
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
}

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
		pendingPerms: make(map[string]*pendingPerm),
		doom:         make(map[string]int),
		indexCh:      make(chan indexJob, 8),
	}
	r.perm = newPermManager(cfg, r.harn, cwd)
	if err := r.initStore(); err != nil {
		return nil, err
	}
	r.startIndexWorker()
	return r, nil
}

// ReplayState emits the current session state for a UI that just attached.
// UI frontends call it after attaching their event consumer: the runner is
// constructed (and the session resumed) before any tap/script handler exists.
func (r *Runner) ReplayState() {
	for _, ev := range r.StateEvents() {
		r.emit(ev)
	}
}

// StateEvents builds the replay for one newly attached client: transcript,
// permission mode, todos, pending approval cards, and staged memory/skill
// changes. The server sends these to a single connection; broadcasting them
// would reset every other attached GUI (session.opened clears their view).
func (r *Runner) StateEvents() []map[string]any {
	r.mu.Lock()
	sess, id := r.sess, r.sessID
	todos := append([]todoItem{}, r.todos...)
	r.mu.Unlock()

	events := []map[string]any{}
	if sess != nil {
		events = append(events, map[string]any{
			"type": "session.opened", "id": id, "title": sess.Title(),
			"blocks": transcriptBlocks(context.Background(), sess),
		})
	}
	events = append(events, map[string]any{"type": "mode.changed", "mode": r.Mode()})
	if len(todos) > 0 {
		events = append(events, r.todoSnapshotEvent())
	}
	events = append(events, r.PendingPermEvents()...)
	events = append(events, r.queueStateEvent())
	if ev := r.gortexEvent(); ev != nil {
		events = append(events, ev)
	}
	if r.mem != nil {
		if ops, err := r.mem.ListPending(); err == nil {
			for _, op := range ops {
				events = append(events, map[string]any{"type": "memory.stage", "op": op})
			}
		}
	}
	if r.skills != nil {
		if ops, err := r.skills.ListPending(); err == nil {
			for _, op := range ops {
				events = append(events, map[string]any{
					"type": "skill.stage", "op": op, "preview": r.skills.Preview(op),
				})
			}
		}
	}
	return events
}

// Send starts a run; at most one run is active.
func (r *Runner) Send(input string) {
	r.StartTask(input)
}

// StartTask starts a run and returns its run id. ok is false when a run is
// already active (a run.busy event is still emitted). Callers that must
// correlate events with the run they started use this instead of Send.
func (r *Runner) StartTask(input string) (id int, ok bool) {
	return r.startTask(input, "", "", false)
}

// StartTaskRef starts a run for a known input (direct send or dequeued item).
// The id/ref travel in run.started so every attached client can reconcile the
// message it rendered optimistically.
func (r *Runner) StartTaskRef(q QueuedInput) (id int, ok bool) {
	return r.startTask(q.Text, q.ID, q.Ref, false)
}

// Continue resumes a stopped/failed turn from the recorded history, with no
// new user message. Every completed tool call stays completed.
func (r *Runner) Continue() (id int, ok bool) {
	return r.startTask("", "", "", true)
}

func (r *Runner) startTask(input, inputID, ref string, cont bool) (id int, ok bool) {
	// A soft-cancel context lets Stop finish the running tool instead of
	// tearing down a write mid-flight; a second Stop uses the hard cancel.
	base, softCancel := dive.WithSoftCancel(context.Background())
	ctx, cancel := context.WithCancel(base)
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		cancel()
		r.bus.Emit(map[string]any{"type": "run.busy"})
		return 0, false
	}
	r.running = true
	r.runSeq++
	if !cont {
		r.lastTask = input
	}
	r.lastAct = time.Now()
	r.cancel, r.softCancel = cancel, softCancel
	r.stopping = false
	id = r.runSeq
	preempt := r.preempt
	r.mu.Unlock()
	if r.review != nil {
		r.review.Interrupt() // foreground preempts background
	}
	if preempt != nil {
		preempt() // improve passes own the GPU too: cancel, they resume later
	}
	go r.run(ctx, id, input, inputID, ref, cont)
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

// Interrupt stops the active run at the next step boundary: running tool
// calls finish, no new one starts. A second Interrupt forces an immediate
// cancel (needed for a wedged tool or a hung model stream).
func (r *Runner) Interrupt() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	if !r.stopping {
		r.stopping = true
		soft := r.softCancel
		r.mu.Unlock()
		if soft != nil {
			soft()
		}
		r.emit(map[string]any{"type": "run.stopping", "mode": "soft"})
		return
	}
	hard := r.cancel
	r.mu.Unlock()
	if hard != nil {
		hard()
	}
	r.emit(map[string]any{"type": "run.stopping", "mode": "hard"})
}

func (r *Runner) run(ctx context.Context, id int, input, inputID, ref string, cont bool) {
	defer r.clearRun(id) // safety net for early returns / panics
	r.resetDoom()
	r.resetPermWait()
	tracer := newPerfTracer(r.bus, id)

	// The hard limit is a backstop (dive's own default is 30 minutes, which
	// used to fire while a permission card was waiting). The active budget
	// below is the control the user sees.
	hard := time.Duration(r.cfg.Agent.TurnHardLimitS) * time.Second
	if hard <= 0 {
		hard = 4 * time.Hour
	}
	ctx, hardCancel := context.WithTimeout(ctx, hard)
	defer hardCancel()
	budget := &turnBudget{}
	runStart := time.Now()
	r.mu.Lock()
	soft := r.softCancel
	r.mu.Unlock()
	stopWatch := r.watchTurnBudget(ctx, runStart,
		time.Duration(r.cfg.Agent.TurnBudgetS)*time.Second, budget, soft)
	defer stopWatch()

	h := r.monitor.Current()
	if h.State == model.StateConnecting {
		r.monitor.ProbeNow() // first-send race: probe synchronously once
		h = r.monitor.Current()
	}
	if !h.IsUsable() {
		r.finishRun(id, false, "", fmt.Sprintf("model server not usable (state: %s)", h.State), 0, 0, 0)
		return
	}

	deltas := newDeltaBatcher(r.bus, id)
	defer deltas.close()

	provider := model.NewProvider(r.cfg.Provider, h.Model)

	r.mu.Lock()
	sess := r.sess
	r.mu.Unlock()
	if !cont && sess.Title() == "" {
		sess.SetTitle(clip(input, 60))
	}

	// TextEditor is intentionally absent: it duplicates Read/Write/Edit, and
	// its schema made every prompt longer and tool choice harder for small
	// models. Read-only tools can run in parallel; anything that changes
	// state is wrapped so a mixed batch stays sequential.
	tools := []dive.Tool{
		toolkit.NewReadFileTool(),
		seq(toolkit.NewWriteFileTool()),
		seq(toolkit.NewEditTool()),
		toolkit.NewGlobTool(),
		toolkit.NewGrepTool(),
		toolkit.NewListDirectoryTool(),
		seq(toolkit.NewBashTool()),
		seq(toolkit.NewFetchTool()),
		seq(r.todoTool()),
	}
	if r.index != nil && r.index.Enabled() {
		tools = append(tools, r.sessionSearchTool())
	}
	if r.mem != nil && r.cfg.Memory.Enabled {
		tools = append(tools, seq(r.memoryTool()))
	}
	if r.skills != nil && r.cfg.Skills.Enabled {
		tools = append(tools, seq(r.skillManageTool()))
	}
	if r.cfg.Agent.Script {
		tools = append(tools, seq(r.scriptTool()))
	}
	skillExt := r.skillLoader()
	var exts []dive.Extension
	if skillExt != nil {
		exts = append(exts, skillExt)
	}
	tools = append(tools, mcpTools(r.mcpManager(), r.isGortexUntracked())...)
	if r.cfg.Agent.Subagents {
		tools = append(tools, seq(r.agentTool(provider, tools)))
	}

	var layers []harness.Layer
	if r.harn != nil && r.cfg.Harness.Enabled {
		layers = r.harn.Layers(r.cfg.Harness.CharLimit)
		layers = append(layers, r.sessionOverlayLayers()...)
	}
	prompt := SystemPrompt(r.cwd, r.Mode(), layers)
	guard := newRepeatGuard()
	hooks := dive.Hooks{
		PreToolUse:         []dive.PreToolUseHook{r.permissionHook},
		PostToolUseFailure: []dive.PostToolUseFailureHook{guard.hook()},
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
	// Budget reminders run after compaction so FinalCall/reminders apply to
	// the call that actually happens. The todo reminder is ModelOnly and
	// appended at the tail, so prompt caching is unaffected.
	hooks.PreIteration = append(hooks.PreIteration, r.todoReminderHook(), budget.hook())

	ag, err := dive.NewAgent(dive.AgentOptions{
		Name:                  "anneal",
		SystemPrompt:          prompt,
		Model:                 provider,
		Tools:                 tools,
		Hooks:                 hooks,
		Extensions:            exts,
		ToolIterationLimit:    r.cfg.Agent.ToolIterationLimit,
		ParallelToolExecution: r.cfg.Agent.ParallelTools,
		Session:               sess,
		ModelSettings:         model.Settings(r.cfg),
		Tracer:                tracer,
	})
	if err != nil {
		r.finishRun(id, false, "", err.Error(), 0, 0, 0)
		return
	}

	// The text/id/ref let every attached client render the user's turn and
	// reconcile the message it showed optimistically.
	r.emit(map[string]any{"type": "run.started", "run": id, "model": h.Model,
		"text": clip(input, 400), "input_id": inputID, "ref": ref, "continue": cont})

	sink := &runEventSink{r: r, run: id, deltas: deltas}
	opts := []dive.CreateResponseOption{dive.WithEventCallback(sink.callback())}
	if cont {
		opts = append(opts, dive.WithContinue())
	} else {
		opts = append(opts, dive.WithInput(input))
	}
	resp, runErr := ag.CreateResponse(ctx, opts...)
	sink.flushThinking()
	sink.flushCompose()
	deltas.flush()
	postStart := time.Now()

	out := map[string]any{"type": "run.finished", "run": id, "ok": runErr == nil,
		"text": "", "error": "", "usage": map[string]any{"tin": 0, "tout": 0}, "ctx_pct": 0}
	if runErr != nil {
		out["error"] = runErr.Error()
	}
	// A user stop is not an error: report it distinctly so the UI can say
	// "stopped" instead of flashing a provider failure. A soft cancel does
	// not cancel the context, so it must be checked separately.
	if ctx.Err() != nil || dive.SoftCanceled(ctx) {
		out["ok"] = false
		out["stopped"] = true
		out["error"] = ""
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
	if oc := turnOutcomeEvent(resp, budget); oc != nil {
		out["outcome"] = oc
	}
	// Persist session-scoped state (title, todo list) — event appends
	// don't rewrite the header.
	r.mu.Lock()
	items := append([]todoItem{}, r.todos...)
	r.mu.Unlock()
	// Always persist: an empty list must overwrite a stale one.
	sess.SetMetadata("todos", items)
	_ = r.store.Put(context.Background(), sess)
	r.invalidateSessions()
	r.queueIndex(sess, h.Model)
	if r.review != nil {
		if gap := r.churnFlag(sess.ID()); gap != "" {
			r.review.Flag(sess.ID(), gap)
			r.emit(map[string]any{"type": "review.flag", "session": sess.ID(), "gap": gap})
		}
		r.review.Enqueue(sess.ID())
	}
	// Report where the time went before announcing completion, so the UI can
	// paint the summary and "Ready" together.
	post := time.Since(postStart)
	sum := tracer.finish(r.takePermWait(), post)
	r.mu.Lock()
	r.lastPerf = &sum
	r.mu.Unlock()
	// Clear before announcing completion: a UI that chains sends on
	// run.finished must not hit run.busy.
	r.clearRun(id)
	r.emit(out)
	// A clean run continues the queue; a stop/error pauses it (the user
	// decides what runs next).
	finishedOK, _ := out["ok"].(bool)
	stopped, _ := out["stopped"].(bool)
	r.startNextQueued(finishedOK && !stopped)
}

// runEventSink translates dive's response items into UI events. Text deltas
// are batched, reasoning is throttled (the UI only shows a status line, and
// one event per reasoning token flooded the bus), and streamed tool-call
// arguments become tool.composing so a long Write is visible while it is
// being written.
type runEventSink struct {
	r      *Runner
	run    int
	deltas *deltaBatcher

	think   string
	thinkAt time.Time

	composeName  string
	composeBytes int
	composeAt    time.Time
}

func (s *runEventSink) callback() dive.EventCallback {
	return func(_ context.Context, item *dive.ResponseItem) error {
		s.handle(item)
		return nil
	}
}

func (s *runEventSink) handle(item *dive.ResponseItem) {
	switch item.Type {
	case dive.ResponseItemTypeModelEvent:
		ev := item.Event
		if ev == nil {
			return
		}
		if ev.Type == llm.EventTypeContentBlockStart && ev.ContentBlock != nil &&
			ev.ContentBlock.Type == llm.ContentTypeToolUse {
			s.composeName = ev.ContentBlock.Name
			s.composeBytes = 0
			s.composeAt = time.Now()
			s.emitCompose()
		}
		d := ev.Delta
		if d == nil {
			return
		}
		if d.Text != "" {
			s.deltas.addText(d.Text)
		}
		if d.Thinking != "" {
			s.think += d.Thinking
			if time.Since(s.thinkAt) >= 250*time.Millisecond {
				s.flushThinking()
			}
		}
		if d.PartialJSON != "" && s.composeName != "" {
			s.composeBytes += len(d.PartialJSON)
			if time.Since(s.composeAt) >= 200*time.Millisecond {
				s.emitCompose()
			}
		}
	case dive.ResponseItemTypeMessage:
		s.flushThinking()
		s.flushCompose()
		s.deltas.flush()
		if item.Message != nil {
			s.r.emit(map[string]any{"type": "message.final", "run": s.run, "text": item.Message.AnswerText()})
		}
	case dive.ResponseItemTypeToolCall:
		s.r.countToolCall()
		s.flushThinking()
		s.flushCompose()
		s.deltas.flush()
		if item.ToolCall != nil {
			s.r.emit(map[string]any{"type": "tool.start", "run": s.run, "id": item.ToolCall.ID,
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
			s.r.emit(map[string]any{"type": "tool.result", "run": s.run, "id": tr.ID,
				"name": tr.Name, "error": isErr, "preview": clip(text, 600)})
		}
	}
}

func (s *runEventSink) flushThinking() {
	if s.think == "" {
		return
	}
	s.r.emit(map[string]any{"type": "thinking.delta", "run": s.run, "text": s.think})
	s.think = ""
	s.thinkAt = time.Now()
}

func (s *runEventSink) emitCompose() {
	s.r.emit(map[string]any{"type": "tool.composing", "run": s.run,
		"name": s.composeName, "bytes": s.composeBytes})
	s.composeAt = time.Now()
}

func (s *runEventSink) flushCompose() {
	s.composeName = ""
	s.composeBytes = 0
}

// clearRun marks run id idle (always before its run.finished emits). A stale
// deferred call from a finished run must not clobber a newer run that the
// event consumer started in the meantime.
func (r *Runner) clearRun(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runSeq != id {
		return
	}
	if r.cancel != nil {
		r.cancel()
	}
	r.running, r.cancel = false, nil
	r.softCancel = nil
	r.stopping = false
	r.lastAct = time.Now()
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
	r.clearRun(id)
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
