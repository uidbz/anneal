// Package script runs agent-authored Risor v2 scripts in a headless,
// capability-scoped sandbox on top of fynerisor's core runtime (no Fyne,
// no exec, no os module, no network). It is the agent's data-glue layer —
// JSON/CSV/string/tabular work — while every external command stays on
// the Bash permission gate.
//
// Each Run gets a fresh fynerisor context (fresh VM), so agent scripts
// never touch the GUI pane VMs and leave no state behind. The tool as a
// whole is permission-gated (ask by default; denied in plan and dontAsk
// modes unless explicitly allowed).
package script

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deepnoodle-ai/risor/v2/pkg/object"
	"github.com/uidbz/fynerisor/core"
)

const (
	// DefaultTimeout bounds one run. The Risor VM checks ctx.Done()
	// periodically, so runaway scripts terminate cleanly.
	DefaultTimeout = 30 * time.Second

	// MaxOutputBytes caps the captured print() output and the JSON dump
	// of the script result fed back to the model.
	MaxOutputBytes = 8192

	// MaxFileBytes caps what one fs.read returns.
	MaxFileBytes = 1 << 20
)

// FreeModules are always enabled in the sandbox; the model may also name
// them in require() declarations or Options.Modules as documentation.
var FreeModules = []string{"json", "csv", "strings", "filepath", "time"}

// HostModules are anneal-provided globals (registered via core.WithGlobal,
// so require("@fs") validates) rather than fynerisor built-ins.
var HostModules = []string{"fs"}

// available lists every module the sandbox offers, for error messages.
func available() string {
	all := append(append([]string{}, FreeModules...), HostModules...)
	sort.Strings(all)
	return strings.Join(all, ", ")
}

// Options configures one run.
type Options struct {
	Script  string
	Modules []string // modules the caller declares (v1: must be a subset of the sandbox set)
	Workdir string   // resolves relative paths; a read+write root
	Timeout time.Duration
}

// Result is what a run produced. Output is the captured print() text;
// ValueJSON is the JSON dump of the script's result value. Both are
// capped; Truncated says when a cap was hit.
type Result struct {
	Value     any
	ValueJSON string
	Output    string
	Truncated bool
}

// capture collects print() output with a byte cap.
type capture struct {
	mu      sync.Mutex
	sb      strings.Builder
	trunc   bool
	maxByte int
}

func (c *capture) builtin() *object.Builtin {
	return object.NewBuiltin("print", func(_ context.Context, args ...object.Object) (object.Object, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.sb.Len() >= c.maxByte {
			c.trunc = true
			return object.Nil, nil
		}
		values := make([]any, 0, len(args))
		for _, a := range args {
			values = append(values, a.Interface())
		}
		fmt.Fprintln(&c.sb, fmt.Sprint(values...))
		return object.Nil, nil
	})
}

func (c *capture) text() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.sb.String()
	trunc := c.trunc
	if len(out) > c.maxByte {
		out = out[:c.maxByte]
		trunc = true
	}
	if trunc {
		out += "\n… [output truncated]"
	}
	return out, trunc
}

// Run compiles and evaluates one script in a fresh sandboxed VM. A
// non-nil Result is returned even when evaluation fails, so callers can
// show the output produced before the failure.
func Run(ctx context.Context, o Options) (*Result, error) {
	if strings.TrimSpace(o.Script) == "" {
		return nil, fmt.Errorf("script: empty script")
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	pol := newPolicy(o.Workdir)

	// Static gate: parse the script (without executing) and check what it
	// declares it needs. A syntax error here means nothing runs.
	reqs, err := core.AnalyzeRequirements(o.Script)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	if err := gate(reqs.RequiredModules, o.Modules); err != nil {
		return nil, err
	}
	if len(reqs.Imports) > 0 {
		return nil, fmt.Errorf("script: import() is not available in this sandbox (imports: %s); inline the code you need", strings.Join(reqs.Imports, ", "))
	}

	out := &capture{maxByte: MaxOutputBytes}
	opts := []core.Option{
		core.WithJSON(),
		core.WithCSV(),
		core.WithStrings(),
		core.WithFilepath(),
		core.WithTime(),
		core.WithGlobal("fs", fsModule(pol)),
		core.WithGlobal("print", out.builtin()),
		core.WithGlobal("import", disabled("import")),
		core.WithGlobal("app", disabled("app")),
	}

	rctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	value, evalErr := core.NewContext(opts...).EvalContext(rctx, o.Script)
	text, trunc := out.text()
	res := &Result{Value: value, Output: text, Truncated: trunc}
	res.ValueJSON = marshalCapped(value, &res.Truncated)
	if evalErr != nil {
		if rctx.Err() == context.DeadlineExceeded {
			return res, fmt.Errorf("script: timed out after %s", o.Timeout)
		}
		return res, fmt.Errorf("script: %s", evalErr)
	}
	return res, nil
}

// gate rejects any module — declared via require() or requested through
// Options.Modules — that the sandbox does not provide.
func gate(declared, requested []string) error {
	for _, m := range append(append([]string{}, declared...), requested...) {
		if !allowed(m) {
			return fmt.Errorf("script: module @%s is not available in this sandbox (available: %s). This tool is for data work only — run commands with the Bash tool and fetch with WebFetch", m, available())
		}
	}
	return nil
}

func allowed(module string) bool {
	for _, m := range FreeModules {
		if m == module {
			return true
		}
	}
	for _, m := range HostModules {
		if m == module {
			return true
		}
	}
	return false
}

// disabled blocks base globals the sandbox must not expose (import(),
// the app object). Returning a Go error makes the VM raise an exception
// and abort the script, so the block cannot be ignored as a value.
func disabled(name string) *object.Builtin {
	return object.NewBuiltin(name, func(_ context.Context, _ ...object.Object) (object.Object, error) {
		return nil, fmt.Errorf("%s is not available in this sandbox", name)
	})
}

// marshalCapped JSON-encodes the result value, falling back to its text
// form for shapes encoding/json cannot handle (non-string map keys).
func marshalCapped(v any, truncated *bool) string {
	if v == nil {
		return ""
	}
	var s string
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err == nil {
		s = strings.TrimSpace(buf.String())
	} else {
		s = fmt.Sprintf("%v", v)
	}
	if len(s) > MaxOutputBytes {
		s = s[:MaxOutputBytes] + "\n… [result truncated]"
		if truncated != nil {
			*truncated = true
		}
	}
	return s
}
