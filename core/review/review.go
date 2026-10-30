// Package review implements Anneal's background self-improvement loop
// (Hermes-style): finished sessions are queued for a review that runs only
// while the user is idle; a small bounded reviewer agent (read-only tools +
// memory + skill_manage + session_search) mines transcript digests for
// durable lessons. Notifications derive from the tool ledger, never from
// model prose (small models happily claim saves they did not make).
package review

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/memory"
	"github.com/uidbz/anneal/core/skills"
	"github.com/uidbz/anneal/core/tieindex"
	"github.com/uidbz/anneal/core/toolset"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/toolkit"
)

// LedgerEntry records one concrete lesson write by the reviewer.
type LedgerEntry struct {
	Kind    string `json:"kind"` // memory | skill
	Target  string `json:"target,omitempty"`
	Action  string `json:"action"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content,omitempty"`
	Result  string `json:"result"`
}

// Event is what the UI/bridge sees.
type Event struct {
	Type    string        `json:"type"` // review.start | review.done | review.skipped
	Session string        `json:"session"`
	Reason  string        `json:"reason"`
	Ledger  []LedgerEntry `json:"ledger"`
	Error   string        `json:"error"`
}

// Manager owns the review queue and the idle scheduler.
type Manager struct {
	cfg *config.Review
	mem *memory.Store
	sk  *skills.Store
	idx *tieindex.Indexer
	// Digest renders one session's transcript for the reviewer (injected by
	// the app layer, which owns the session store). Bounded by MaxInputTokens.
	Digest func(ctx context.Context, sessionID string) string
	// Model builds the reviewer's LLM lazily (same server, budgeted input).
	Model func() llm.LLM

	emit func(any) // bridge passthrough (Event values + stage cards)

	flags *FlagStore // harness-deficiency queue (M16; nil = disabled)

	wake chan struct{} // scheduler wake-up (created once; never reassigned)

	mu       sync.Mutex
	queue    []string
	firstQue time.Time
	running  bool // review currently executing
	busy     func() bool
	lastAct  func() time.Time
	cancel   context.CancelFunc
}

func NewManager(cfg *config.Review, mem *memory.Store, sk *skills.Store,
	idx *tieindex.Indexer, emit func(any), model func() llm.LLM) *Manager {
	if cfg == nil {
		d := config.Default().Review
		cfg = &d
	}
	return &Manager{cfg: cfg, mem: mem, sk: sk, idx: idx, emit: emit, Model: model,
		wake:    make(chan struct{}, 1),
		Digest:  func(context.Context, string) string { return "" },
		busy:    func() bool { return false },
		lastAct: func() time.Time { return time.Now() }}
}

// signal wakes the scheduler without blocking (coalesced).
func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Running reports whether a review pass is currently executing. Other GPU
// consumers (auto-improve) use it to stay out of the reviewer's way.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// SetActivityHooks wires the "is the user active" probes from the runner.
func (m *Manager) SetActivityHooks(busy func() bool, lastActivity func() time.Time) {
	m.mu.Lock()
	m.busy, m.lastAct = busy, lastActivity
	m.mu.Unlock()
}

// SetFlagStore enables the harness_flag tool (reviewer -> improve queue).
func (m *Manager) SetFlagStore(fs *FlagStore) { m.flags = fs }

// ConsumeFlags marks up to n pending flags as consumed by improve runid.
func (m *Manager) ConsumeFlags(runid string, n int) []Flag {
	if m.flags == nil {
		return nil
	}
	return m.flags.Consume(runid, n)
}

// ReleaseFlags un-consumes flags when an improve step fails before using them.
func (m *Manager) ReleaseFlags(runid string) {
	if m.flags == nil {
		return
	}
	m.flags.Release(runid)
}

// PendingFlags peeks at the queue (oldest first).
func (m *Manager) PendingFlags(n int) []Flag {
	if m.flags == nil {
		return nil
	}
	return m.flags.Pending(n)
}

// Enqueue records sessions for later review (deduped).
func (m *Manager) Enqueue(sessionIDs ...string) {
	if !m.cfg.Enabled {
		return
	}
	m.mu.Lock()
	for _, id := range sessionIDs {
		if id == "" {
			continue
		}
		dup := false
		for _, s := range m.queue {
			if s == id {
				dup = true
				break
			}
		}
		if !dup {
			m.queue = append(m.queue, id)
		}
	}
	if len(m.queue) > 0 && m.firstQue.IsZero() {
		m.firstQue = time.Now()
	}
	m.mu.Unlock()
	m.signal()
}

// LearnNow triggers a manual review pass as soon as the agent is idle
// (idle-settle wait is bypassed for the queued batch).
func (m *Manager) LearnNow() {
	m.mu.Lock()
	m.firstQue = time.Now().Add(-time.Duration(m.cfg.IdleSettleS+1) * time.Second)
	m.mu.Unlock()
	m.signal()
}

// Interrupt preempts a running review; its batch is requeued.
func (m *Manager) Interrupt() {
	m.mu.Lock()
	c := m.cancel
	m.mu.Unlock()
	if c != nil {
		c()
	}
}

// Start runs the scheduler loop until ctx is done.
func (m *Manager) Start(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-tick.C:
		}
		for m.shouldRun() {
			m.runOnce(ctx)
		}
	}
}

func (m *Manager) shouldRun() bool {
	m.mu.Lock()
	n, running := len(m.queue), m.running
	first := m.firstQue
	busy, lastAct := m.busy, m.lastAct
	m.mu.Unlock()
	if n == 0 || running {
		return false
	}
	if busy() {
		return false
	}
	if m.cfg.DeferUntilIdle {
		settle := time.Duration(m.cfg.IdleSettleS) * time.Second
		forced := time.Since(first) >= time.Duration(m.cfg.MaxAgeS)*time.Second
		if !forced && time.Since(lastAct()) < settle {
			return false
		}
	}
	return true
}

func (m *Manager) runOnce(ctx context.Context) {
	m.mu.Lock()
	batch := m.queue
	m.queue = nil
	m.firstQue = time.Time{}
	m.running = true
	m.mu.Unlock()

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()

	sessions := strings.Join(batch, ",")
	m.emit(Event{Type: "review.start", Session: sessions, Ledger: []LedgerEntry{}})

	ledger := []LedgerEntry{}
	digest := ""
	for _, sid := range batch {
		if sid == "" {
			continue
		}
		digest += m.Digest(rctx, sid) + "\n---\n"
		if len(digest) > m.cfg.MaxInputTokens*4 { // ~4 bytes/token
			break
		}
	}

	var runErr, agentErr error
	if strings.TrimSpace(digest) == "" {
		m.emit(Event{Type: "review.skipped", Session: sessions, Reason: "empty digest", Ledger: ledger})
		m.finishRun()
		return
	}
	tools := m.reviewerTools(&ledger, sessions)
	var rev *dive.Agent
	rev, agentErr = dive.NewAgent(dive.AgentOptions{
		Name:               "anneal-review",
		SystemPrompt:       reviewerPrompt,
		Model:              m.Model(),
		Tools:              tools,
		ToolIterationLimit: 6,
	})
	if agentErr == nil {
		_, runErr = rev.CreateResponse(rctx, dive.WithInput("Transcript digest(s) follow.\n\n"+digest))
	}

	preempted := rctx.Err() != nil
	m.finishRun()

	if preempted {
		m.Enqueue(batch...)
		m.emit(Event{Type: "review.skipped", Session: sessions, Reason: "preempted by user", Ledger: ledger})
		return
	}
	evt := Event{Type: "review.done", Session: sessions, Ledger: ledger}
	if runErr != nil {
		evt.Error = runErr.Error()
	}
	if agentErr != nil {
		evt.Error = agentErr.Error()
	}
	m.emit(evt)
}

// finishRun marks the manager idle (also clears the interrupt hook).
func (m *Manager) finishRun() {
	m.mu.Lock()
	m.running = false
	m.cancel = nil
	m.mu.Unlock()
}

const reviewerPrompt = `You are Anneal's background self-improvement reviewer. You are given digest(s) of recent conversation transcripts (user messages, assistant replies, tool calls).

Your job: mine them for things worth remembering, and save them:
- Corrections the user made, conventions they stated, environment/project facts learned, user preferences -> memory tool (target 'memory' or 'user'). Keep each entry one short line.
- Multi-step workflows that will recur (with commands/gotchas) -> skill_manage create (or patch an existing skill when you learn improvements). Check skill_manage list first.
- Stale/wrong existing memories: patch or remove them.
- Recurring WORK-STYLE deficiencies (done declared without verifying, the same failing call repeated, work started without a plan, files never re-read): call harness_flag with one short line describing the deficiency. harness_flag is a QUEUE for the separate improve loop — you must never edit or propose harness files yourself, and do not put task-specific facts in flags (facts go to memory).

Rules: be selective (a few high-value lessons beat many trivia); do not store conversation play-by-play; no secrets/credentials; you may use Read/Glob/Grep/ListDirectory/session_search to verify facts, nothing that mutates the workspace. When done, reply with a one-line summary of what you saved (or 'nothing worth saving').`

func (m *Manager) reviewerTools(ledger *[]LedgerEntry, sessions string) []dive.Tool {
	tools := []dive.Tool{
		toolkit.NewReadFileTool(),
		toolkit.NewGlobTool(),
		toolkit.NewGrepTool(),
		toolkit.NewListDirectoryTool(),
	}
	tools = append(tools, toolset.MemoryTool(m.mem, "review", toolset.MemoryCallbacks{
		Stage: func(_ *memory.Op, staged *memory.Op) {
			*ledger = append(*ledger, LedgerEntry{Kind: "memory", Target: staged.Target,
				Action: staged.Action, Content: staged.Content, Result: "staged " + staged.ID})
			m.emit(map[string]any{"type": "memory.stage", "op": staged})
		},
		Apply: func(op *memory.Op, content string) {
			*ledger = append(*ledger, LedgerEntry{Kind: "memory", Target: op.Target,
				Action: op.Action, Content: op.Content, Result: fmt.Sprintf("applied (%d chars)", len(content))})
		},
	}))

	tools = append(tools, toolset.SkillTool(m.sk, "review", toolset.SkillCallbacks{
		Stage: func(op, staged *skills.Op) {
			*ledger = append(*ledger, LedgerEntry{Kind: "skill", Action: op.Action,
				Name: op.Name, Result: "staged " + staged.ID})
			m.emit(map[string]any{"type": "skill.stage", "op": staged})
		},
		Apply: func(op *skills.Op, result string) {
			*ledger = append(*ledger, LedgerEntry{Kind: "skill", Action: op.Action,
				Name: op.Name, Result: result})
		},
	}))

	if m.flags != nil {
		type flagIn struct {
			Gap string `json:"gap"`
		}
		tools = append(tools, dive.FuncTool("harness_flag",
			"Queue one recurring work-style deficiency (verification gaps, repeated failing actions, planning gaps) for the harness improve loop. One short line; never task facts; you never edit harness files.",
			func(ctx context.Context, in *flagIn) (*dive.ToolResult, error) {
				gap := strings.TrimSpace(in.Gap)
				if len(gap) < 8 {
					return dive.NewToolResultText("harness_flag: gap too short"), nil
				}
				f := m.flags.Add(sessions, gap)
				*ledger = append(*ledger, LedgerEntry{Kind: "harness-flag", Action: "add",
					Name: f.ID, Content: gap, Result: "queued"})
				m.emit(map[string]any{"type": "review.flag", "id": f.ID,
					"gap": gap, "session": sessions})
				return dive.NewToolResultText("flagged for the improve loop (" + f.ID + ")."), nil
			},
			dive.WithFuncToolAnnotations(&dive.ToolAnnotations{ReadOnlyHint: true, Title: "Flag harness gap (review)"})))
	}

	if m.idx != nil && m.idx.Enabled() {
		type qIn struct {
			Query string `json:"query"`
			Limit int    `json:"limit,omitempty"`
		}
		tools = append(tools, dive.FuncTool("session_search",
			"Search past sessions for context.",
			func(ctx context.Context, in *qIn) (*dive.ToolResult, error) {
				hits, err := m.idx.Search(in.Query, in.Limit)
				if err != nil {
					return dive.NewToolResultText("session_search: " + err.Error()), nil
				}
				var sb strings.Builder
				for _, h := range hits {
					fmt.Fprintf(&sb, "- [%s] %s %s: %s\n", h.SessionID, h.MsgAt, h.Role, h.Snippet)
				}
				return dive.NewToolResultText(sb.String()), nil
			},
			dive.WithFuncToolAnnotations(&dive.ToolAnnotations{ReadOnlyHint: true, Title: "Session search (review)"})))
	}
	return tools
}
