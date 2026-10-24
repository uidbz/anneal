// Package agent composes and runs the dive agent for fyagent.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"fyagent/core/config"
	"fyagent/core/model"
	"fyagent/core/uiapi"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/session"
	"github.com/deepnoodle-ai/dive/toolkit"
)

// Runner owns the conversation session and executes one task turn at a time.
type Runner struct {
	cfg     *config.Config
	monitor *model.Monitor
	bridge  *uiapi.Bridge
	cwd     string

	mu       sync.Mutex
	sess     *session.Session
	cancel   context.CancelFunc
	runSeq   int
	running  bool
}

func NewRunner(cfg *config.Config, monitor *model.Monitor, bridge *uiapi.Bridge, cwd string) *Runner {
	return &Runner{cfg: cfg, monitor: monitor, bridge: bridge, cwd: cwd, sess: session.New("current")}
}

// Send starts a run; at most one run is active.
func (r *Runner) Send(input string) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		r.bridge.Emit(map[string]any{"type": "run.busy"})
		return
	}
	r.running = true
	r.runSeq++
	id := r.runSeq
	r.mu.Unlock()
	go r.run(id, input)
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
	defer func() {
		r.mu.Lock()
		r.running, r.cancel = false, nil
		r.mu.Unlock()
	}()

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

	deltas := newDeltaBatcher(r.bridge, id)
	defer deltas.close()

	provider := model.NewProvider(r.cfg.Provider, h.Model)

	agent, err := dive.NewAgent(dive.AgentOptions{
		Name:         "fyagent",
		SystemPrompt: SystemPrompt(r.cwd),
		Model:        provider,
		Tools: []dive.Tool{
			toolkit.NewReadFileTool(),
			toolkit.NewWriteFileTool(),
			toolkit.NewEditTool(),
			toolkit.NewTextEditorTool(),
			toolkit.NewGlobTool(),
			toolkit.NewGrepTool(),
			toolkit.NewListDirectoryTool(),
			toolkit.NewBashTool(),
			toolkit.NewFetchTool(),
		},
		ToolIterationLimit: r.cfg.Agent.ToolIterationLimit,
		Session:            r.sess,
		ModelSettings: &dive.ModelSettings{
			MaxTokens:   dive.Ptr(r.cfg.Provider.MaxTokens),
			Temperature: dive.Ptr(r.cfg.Provider.Temperature),
		},
	})
	if err != nil {
		r.finishRun(id, false, "", err.Error(), 0, 0, 0)
		return
	}

	r.emit(map[string]any{"type": "run.started", "run": id, "model": h.Model})

	resp, runErr := agent.CreateResponse(ctx,
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
	r.emit(out)
}

func (r *Runner) finishRun(id int, ok bool, text, errMsg string, in, outTok, ctxPct int) {
	r.emit(map[string]any{"type": "run.finished", "run": id, "ok": ok,
		"text": text, "error": errMsg,
		"usage": map[string]any{"tin": in, "tout": outTok}, "ctx_pct": ctxPct})
}

func (r *Runner) emit(v any) { r.bridge.Emit(v) }

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
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// deltaBatcher coalesces text deltas so the script handler sees ≤ ~20/s.
type deltaBatcher struct {
	bridge  *uiapi.Bridge
	run     int
	mu      sync.Mutex
	buf     string
	last    time.Time
	pending *time.Timer
}

func newDeltaBatcher(b *uiapi.Bridge, run int) *deltaBatcher {
	return &deltaBatcher{bridge: b, run: run}
}

const (
	batchChars  = 240
	batchIdle   = 60 * time.Millisecond
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
	d.bridge.Emit(map[string]any{"type": "text.delta", "run": d.run, "text": d.buf})
	d.buf = ""
}

func (d *deltaBatcher) close() {
	d.flush()
}
