package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fyagent/core/config"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/toolkit"
)

// Options configures one replay execution.
type Options struct {
	Name          string // "reference" | "probe-<candidate>" | "successor"
	Cfg           *config.Config
	Model         func() llm.LLM
	ModelID       string // for the ConfigHash audit
	System        string // full system prompt (base + harness layers)
	Task          string // task text; paths must already be rewritten to the sandbox
	Scratch       *Sandbox
	OutDir        string // record written to <OutDir>/<Name>.json (optional)
	HarnessHash   string
	MaxIterations int
	WallTimeoutS  int
	Emit          func(any) // progress events (may be nil)
}

// ToolStep is one entry of the condensed tool ledger (trajectory τ).
type ToolStep struct {
	Name    string `json:"name"`
	Input   string `json:"input"`
	Error   bool   `json:"error"`
	Preview string `json:"preview"`
}

// Record is one execution e = (τ, y) plus audit fields.
type Record struct {
	Name        string     `json:"name"`
	Task        string     `json:"task"`
	Src         string     `json:"src,omitempty"`
	SandboxWS   string     `json:"sandbox_ws,omitempty"`
	ConfigHash  string     `json:"config_hash"`
	HarnessHash string     `json:"harness_hash"`
	Steps       []ToolStep `json:"steps"`
	Deliverable string     `json:"deliverable"`
	Error       string     `json:"error,omitempty"`
	Tin         int        `json:"tin"`
	Tout        int        `json:"tout"`
	DurationS   float64    `json:"duration_s"`
	StartedAt   time.Time  `json:"started_at"`
}

// clip truncates a string to n chars.
func clip(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Run executes the task once with the base toolkit (no memory/skills/
// subagents/MCP — probes test harness-file effects, not accumulated state)
// inside the sandbox jail when one is given. It always returns a record;
// the error is the run error (also stored in Record.Error).
func Run(ctx context.Context, opts Options) (*Record, error) {
	if opts.MaxIterations <= 0 {
		opts.MaxIterations = 40
	}
	if opts.WallTimeoutS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(opts.WallTimeoutS)*time.Second)
		defer cancel()
	}
	rec := &Record{
		Name: opts.Name, Task: opts.Task, ConfigHash: ConfigHash(opts.Cfg, opts.ModelID, opts.MaxIterations),
		HarnessHash: opts.HarnessHash, StartedAt: time.Now(), Steps: []ToolStep{},
	}
	if opts.Scratch != nil {
		rec.Src, rec.SandboxWS = opts.Scratch.Src, opts.Scratch.WS
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
	}
	var hooks dive.Hooks
	if opts.Scratch != nil {
		// dive-native workspace jail first…
		ws := opts.Scratch.WS
		tools = []dive.Tool{
			toolkit.NewReadFileTool(toolkit.ReadFileToolOptions{WorkspaceDir: ws}),
			toolkit.NewWriteFileTool(toolkit.WriteFileToolOptions{WorkspaceDir: ws}),
			toolkit.NewEditTool(toolkit.EditToolOptions{WorkspaceDir: ws}),
			toolkit.NewTextEditorTool(toolkit.TextEditorToolOptions{WorkspaceDir: ws}),
			toolkit.NewGlobTool(toolkit.GlobToolOptions{WorkspaceDir: ws}),
			toolkit.NewGrepTool(toolkit.GrepToolOptions{WorkspaceDir: ws}),
			toolkit.NewListDirectoryTool(toolkit.ListDirectoryToolOptions{WorkspaceDir: ws}),
			toolkit.NewBashTool(toolkit.BashToolOptions{WorkspaceDir: ws}),
			toolkit.NewFetchTool(),
		}
		// …plus our hook as defense-in-depth (protects src + fyagent state
		// even if a tool is ever swapped in without WorkspaceDir).
		hooks.PreToolUse = append(hooks.PreToolUse, opts.Scratch.Jail())
	}

	ag, err := dive.NewAgent(dive.AgentOptions{
		Name:               "probe-" + opts.Name,
		SystemPrompt:       opts.System,
		Model:              opts.Model(),
		Tools:              tools,
		Hooks:              hooks,
		ToolIterationLimit: opts.MaxIterations,
		ModelSettings: &dive.ModelSettings{
			MaxTokens:   dive.Ptr(opts.Cfg.Provider.MaxTokens),
			Temperature: dive.Ptr(opts.Cfg.Provider.Temperature),
		},
	})
	if err != nil {
		rec.Error = err.Error()
		return rec, err
	}

	inputs := map[string]string{}
	emit := opts.Emit
	if emit == nil {
		emit = func(any) {}
	}

	resp, runErr := ag.CreateResponse(ctx,
		dive.WithInput(opts.Task),
		dive.WithEventCallback(func(_ context.Context, item *dive.ResponseItem) error {
			switch item.Type {
			case dive.ResponseItemTypeToolCall:
				if item.ToolCall != nil {
					inputs[item.ToolCall.ID] = clip(string(item.ToolCall.Input), 300)
					emit(map[string]any{"type": "probe.step", "name": opts.Name,
						"tool": item.ToolCall.Name, "input": inputs[item.ToolCall.ID]})
				}
			case dive.ResponseItemTypeToolCallResult:
				if tr := item.ToolCallResult; tr != nil {
					text, isErr := resultText(tr)
					rec.Steps = append(rec.Steps, ToolStep{
						Name: tr.Name, Input: inputs[tr.ID], Error: isErr, Preview: clip(text, 400),
					})
					emit(map[string]any{"type": "probe.step", "name": opts.Name,
						"tool": tr.Name, "error": isErr, "preview": clip(text, 200)})
				}
			}
			return nil
		}),
	)
	rec.Deliverable = ""
	if resp != nil {
		rec.Deliverable = resp.OutputText()
		rec.Tin, rec.Tout = respUsage(resp)
	}
	if runErr != nil {
		rec.Error = runErr.Error()
	}
	rec.DurationS = time.Since(rec.StartedAt).Seconds()

	if opts.OutDir != "" {
		if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
			return rec, err
		}
		b, _ := json.MarshalIndent(rec, "", "  ")
		if err := os.WriteFile(filepath.Join(opts.OutDir, opts.Name+".json"), b, 0o644); err != nil {
			return rec, err
		}
	}
	return rec, runErr
}

func resultText(tr *dive.ToolCallResult) (string, bool) {
	isErr := tr.Error != nil || (tr.Result != nil && tr.Result.IsError)
	if tr.Error != nil {
		return tr.Error.Error(), true
	}
	if tr.Result == nil {
		return "", isErr
	}
	var sb []byte
	for _, c := range tr.Result.Content {
		if c != nil && c.Type == dive.ToolResultContentTypeText {
			sb = append(sb, c.Text...)
		}
	}
	if len(sb) == 0 {
		b, _ := json.Marshal(tr.Result)
		return string(b), isErr
	}
	return string(sb), isErr
}

func respUsage(resp *dive.Response) (int, int) {
	if resp == nil || resp.Usage == nil {
		return 0, 0
	}
	return resp.Usage.InputTokens, resp.Usage.OutputTokens
}

// Digest renders the execution as bounded editor-facing text.
func (r *Record) Digest(maxChars int) string {
	var sb []byte
	add := func(s string) { sb = append(sb, s...) }
	add("task: " + clip(r.Task, 500) + "\n")
	if r.Error != "" {
		add("run error: " + r.Error + "\n")
	}
	add("tools:\n")
	for _, s := range r.Steps {
		status := "ok"
		if s.Error {
			status = "ERROR"
		}
		add(fmt.Sprintf("- %s %s → %s %s\n", s.Name, clip(s.Input, 200), status, clip(s.Preview, 200)))
	}
	add("deliverable:\n" + clip(r.Deliverable, 2000) + "\n")
	out := string(sb)
	if maxChars > 0 && len(out) > maxChars {
		out = out[:maxChars] + "\n… [digest truncated]"
	}
	return out
}
