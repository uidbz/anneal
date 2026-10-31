package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/experimental/mcp"
	"github.com/deepnoodle-ai/dive/llm"
)

type captureTool struct {
	got any
}

func (c *captureTool) Name() string                       { return "cap" }
func (c *captureTool) Description() string                { return "" }
func (c *captureTool) Schema() *dive.Schema               { return nil }
func (c *captureTool) Annotations() *dive.ToolAnnotations { return nil }
func (c *captureTool) Call(ctx context.Context, input any) (*dive.ToolResult, error) {
	c.got = input
	return dive.NewToolResultText("ok"), nil
}

// The wrapper must rename but pass input through untouched — the mcp
// adapter handles dive's []byte input natively since v1.33.0.
func TestQualifiedToolPassThrough(t *testing.T) {
	cap := &captureTool{}
	q := &qualifiedTool{base: cap, name: mcp.QualifiedToolName("demo", "echo")}
	if q.Name() != "mcp__demo__echo" {
		t.Fatalf("qualified name: %s", q.Name())
	}
	raw := json.RawMessage(`{"text":"hello mcp"}`)
	if _, err := q.Call(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	g, ok := cap.got.(json.RawMessage)
	if !ok || string(g) != `{"text":"hello mcp"}` {
		t.Fatalf("input rewritten: %T %v", cap.got, cap.got)
	}
}

func mcpResultText(res *dive.ToolResult) string {
	if res == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range res.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

// TestMCPErrorHint: "repository not tracked" must carry the fix and forbid the
// unchanged retry (issue #13).
func TestMCPErrorHint(t *testing.T) {
	res := dive.NewToolResultError("repository not tracked: /home/johan")
	out, err := mcpErrorHint("mcp__gortex__explore", res, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := mcpResultText(out)
	for _, want := range []string{"gortex track", "Do not retry", "Grep/Glob"} {
		if !strings.Contains(text, want) {
			t.Fatalf("hint missing %q:\n%s", want, text)
		}
	}
	// Error-returning calls get the hint appended too.
	_, err = mcpErrorHint("mcp__gortex__explore", nil, errors.New("repository not tracked: /x"))
	if err == nil || !strings.Contains(err.Error(), "Do not retry") {
		t.Fatalf("error hint = %v", err)
	}
	// Other errors are untouched.
	res2 := dive.NewToolResultError("boom")
	out2, _ := mcpErrorHint("mcp__other__x", res2, nil)
	if strings.Contains(mcpResultText(out2), "gortex") {
		t.Fatal("unrelated error gained a gortex hint")
	}
}

func TestRepeatGuard(t *testing.T) {
	g := newRepeatGuard()
	hook := g.hook()
	call := func(name string) *dive.HookContext {
		return &dive.HookContext{
			Call:   &llm.ToolUseContent{Name: name},
			Result: &dive.ToolCallResult{Error: errors.New("repository not tracked")},
		}
	}
	h1 := call("mcp__gortex__explore")
	if err := hook(context.Background(), h1); err != nil {
		t.Fatal(err)
	}
	if h1.AdditionalContext != "" {
		t.Fatal("first failure was flagged")
	}
	h2 := call("mcp__gortex__explore")
	_ = hook(context.Background(), h2)
	if !strings.Contains(h2.AdditionalContext, "failed the same way 2 times") {
		t.Fatalf("second failure context = %q", h2.AdditionalContext)
	}
	// A different failure fingerprint does not share the counter.
	h3 := &dive.HookContext{Call: &llm.ToolUseContent{Name: "mcp__gortex__explore"},
		Result: &dive.ToolCallResult{Error: errors.New("timeout")}}
	_ = hook(context.Background(), h3)
	if h3.AdditionalContext != "" {
		t.Fatal("a different failure was flagged")
	}
}

func TestGortexStatus(t *testing.T) {
	r := &Runner{}
	if r.isGortexUntracked() {
		t.Fatal("untracked before detection")
	}
	r.SetGortexStatus(true, []string{"/repo/a"})
	if !r.isGortexUntracked() {
		t.Fatal("untracked status lost")
	}
	ev := r.gortexEvent()
	if ev == nil || ev["type"] != "gortex.untracked" {
		t.Fatalf("event = %+v", ev)
	}
	r.SetGortexStatus(false, []string{"/repo/a"})
	if r.gortexEvent() != nil {
		t.Fatal("tracked repo still reports an untracked event")
	}
}
