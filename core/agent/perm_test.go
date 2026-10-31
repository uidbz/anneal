package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/events"
	"github.com/uidbz/anneal/core/permission"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

func TestDefaultPermissionConfig(t *testing.T) {
	t.Setenv("ANNEAL_HOME", t.TempDir())
	cfg := config.Default()
	m := newPermManager(cfg, nil, t.TempDir())

	check := func(action string, resources []string, want permission.Effect) {
		t.Helper()
		got := m.Evaluate(permission.Request{Action: action, Resources: resources}).Effect
		if got != want {
			t.Errorf("%s %v = %s, want %s", action, resources, got, want)
		}
	}
	check("read", []string{"main.go"}, permission.Allow)
	check("read", []string{".env"}, permission.Ask)
	check("read", []string{"config/.env.local"}, permission.Ask)
	check("read", []string{".env.example"}, permission.Allow)
	check("bash", []string{"ls"}, permission.Ask)
	check("edit", []string{"main.go"}, permission.Ask)
	check("webfetch", []string{"https://example.com"}, permission.Ask)
	check("mcp__gortex__explore", []string{"*"}, permission.Ask)
}

func TestPermissionConfigOverridesLegacyDefaults(t *testing.T) {
	t.Setenv("ANNEAL_HOME", t.TempDir())
	cfg := config.Default()
	// OpenCode-style "allow everything, then deny pushes" should override the
	// legacy ask rule for Bash because permission rules come later.
	cfg.Permission = permission.Config{
		{Action: "*", Resource: "*", Effect: permission.Allow},
		{Action: "bash", Resource: "git push *", Effect: permission.Deny},
	}
	cfg.Agent.Permission = permission.Config{
		{Action: "bash", Resource: "rm *", Effect: permission.Deny},
	}
	m := newPermManager(cfg, nil, t.TempDir())
	if got := m.Evaluate(permission.Request{Action: "bash", Resources: []string{"ls"}}).Effect; got != permission.Allow {
		t.Fatalf("global allow: %s", got)
	}
	if got := m.Evaluate(permission.Request{Action: "bash", Resources: []string{"git push"}}).Effect; got != permission.Deny {
		t.Fatalf("global deny: %s", got)
	}
	if got := m.Evaluate(permission.Request{Action: "bash", Resources: []string{"rm -rf /"}}).Effect; got != permission.Deny {
		t.Fatalf("agent deny: %s", got)
	}
}

func TestInspectReadAndExternal(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Runner{cwd: root}

	intent := r.inspectCall("Read", json.RawMessage(`{"file_path":"a.txt"}`), true)
	if intent.action != "read" || len(intent.resources) != 1 || intent.resources[0] != "a.txt" {
		t.Fatalf("inside read: %+v", intent)
	}
	if len(intent.external) != 0 {
		t.Fatalf("inside read marked external: %+v", intent.external)
	}

	secret := filepath.Join(outside, "secret.txt")
	intent = r.inspectCall("Read", json.RawMessage(`{"file_path":`+quote(secret)+`}`), true)
	if intent.action != "read" || intent.resources[0] != secret {
		t.Fatalf("outside read resource: %+v", intent)
	}
	if len(intent.external) != 1 {
		t.Fatalf("outside read not external: %+v", intent)
	}
	if want := filepath.ToSlash(outside) + "/*"; intent.external[0].resource != want {
		t.Fatalf("external resource = %q, want %q", intent.external[0].resource, want)
	}

	intent = r.inspectCall("Write", json.RawMessage(`{"file_path":"new/dir/a.txt"}`), false)
	if intent.action != "edit" || len(intent.external) != 0 {
		t.Fatalf("inside write: %+v", intent)
	}
}

func TestInspectBashCompound(t *testing.T) {
	root := t.TempDir()
	r := &Runner{cwd: root}
	intent := r.inspectCall("Bash", json.RawMessage(`{"command":"git status && rm -rf build"}`), false)
	if intent.action != "bash" {
		t.Fatalf("action: %+v", intent)
	}
	if strings.Join(intent.resources, "|") != "git status|rm -rf build" {
		t.Fatalf("resources: %+v", intent.resources)
	}
	if strings.Join(intent.save, "|") != "git status *|rm *" {
		t.Fatalf("save: %+v", intent.save)
	}
}

func TestDoomLoopTripsEveryThirdCall(t *testing.T) {
	r := &Runner{doom: map[string]int{}}
	input := []byte(`{"file_path":"a"}`)
	for i := 1; i <= 6; i++ {
		got := r.doomTripped("Read", input)
		want := i%3 == 0
		if got != want {
			t.Fatalf("call %d = %v, want %v", i, got, want)
		}
	}
	r.resetDoom()
	if r.doomTripped("Read", input) {
		t.Fatal("reset did not clear counts")
	}
}

func TestRespondPermAlwaysSavesAndReleasesPending(t *testing.T) {
	t.Setenv("ANNEAL_HOME", t.TempDir())
	root := t.TempDir()
	cfg := config.Default()
	r := &Runner{
		cfg:          cfg,
		cwd:          root,
		pendingPerms: map[string]*pendingPerm{},
		doom:         map[string]int{},
	}
	r.perm = newPermManager(cfg, nil, root)

	first := permission.Request{
		Action: "bash", Resources: []string{"git status"},
		Save: []string{"git status *"}, Tool: "Bash",
	}
	second := permission.Request{
		Action: "bash", Resources: []string{"git status --short"},
		Save: []string{"git status *"}, Tool: "Bash",
	}
	if got := r.perm.Evaluate(first).Effect; got != permission.Ask {
		t.Fatalf("first should ask: %s", got)
	}
	if got := r.perm.Evaluate(second).Effect; got != permission.Ask {
		t.Fatalf("second should ask: %s", got)
	}

	id1, ch1 := r.registerPerm(first)
	id2, ch2 := r.registerPerm(second)
	r.RespondPerm(id1, "session")

	if got := <-ch1; got != permission.Always {
		t.Fatalf("first outcome: %s", got)
	}
	if got := <-ch2; got != permission.Once {
		t.Fatalf("second outcome: %s", got)
	}
	if _, ok := r.pendingPerms[id2]; ok {
		t.Fatal("auto-allowed request still pending")
	}
	if got := r.perm.Evaluate(second).Effect; got != permission.Allow {
		t.Fatalf("saved rule: %s", got)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

type fakePermTool struct {
	name string
	ro   bool
}

func (t *fakePermTool) Name() string         { return t.name }
func (t *fakePermTool) Description() string  { return "fake" }
func (t *fakePermTool) Schema() *dive.Schema { return nil }
func (t *fakePermTool) Annotations() *dive.ToolAnnotations {
	return &dive.ToolAnnotations{ReadOnlyHint: t.ro}
}
func (t *fakePermTool) Call(context.Context, any) (*dive.ToolResult, error) {
	return dive.NewToolResultText("ok"), nil
}

func TestPermissionHookBlocksUntilAnswered(t *testing.T) {
	t.Setenv("ANNEAL_HOME", t.TempDir())
	root := t.TempDir()
	cfg := config.Default()
	bus := events.New()
	reqs := make(chan map[string]any, 4)
	bus.AddTap(func(v any) {
		if m, ok := v.(map[string]any); ok && m["type"] == "perm.request" {
			reqs <- m
		}
	})
	r := &Runner{
		cfg:          cfg,
		bus:          bus,
		cwd:          root,
		pendingPerms: map[string]*pendingPerm{},
		doom:         map[string]int{},
	}
	r.perm = newPermManager(cfg, nil, root)

	done := make(chan error, 1)
	go func() {
		done <- r.permissionHook(context.Background(), &dive.HookContext{
			Tool: &fakePermTool{name: "Bash"},
			Call: &llm.ToolUseContent{Name: "Bash", Input: json.RawMessage(`{"command":"echo hi"}`)},
		})
	}()

	var req map[string]any
	select {
	case req = <-reqs:
	case <-time.After(2 * time.Second):
		t.Fatal("no perm.request event")
	}
	if req["action"] != "bash" {
		t.Fatalf("action = %v", req["action"])
	}
	if got := req["resources"].([]string); len(got) != 1 || got[0] != "echo hi" {
		t.Fatalf("resources = %v", req["resources"])
	}
	r.RespondPerm(req["id"].(string), "allow")

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("hook returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook did not unblock")
	}
}

// TestTextEditorToolMapsToEdit: dive registers its text editor as
// str_replace_based_edit_tool. The mapper only knew "TextEditor", so those
// calls skipped path resolution, edit rules and the outside-project check.
func TestTextEditorToolMapsToEdit(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{cwd: dir}
	for _, name := range []string{"TextEditor", "str_replace_based_edit_tool"} {
		in := json.RawMessage(`{"command":"create","path":"oops.txt","file_text":"x"}`)
		intent := r.inspectCall(name, in, false)
		if intent.action != "edit" {
			t.Fatalf("%s: action = %q, want edit", name, intent.action)
		}
		if len(intent.resources) != 1 || intent.resources[0] != "oops.txt" {
			t.Fatalf("%s: resources = %v", name, intent.resources)
		}
		// A path outside the project must still raise the external check.
		out := json.RawMessage(`{"command":"create","path":"/etc/passwd","file_text":"x"}`)
		intent = r.inspectCall(name, out, false)
		if len(intent.external) == 0 {
			t.Fatalf("%s: outside path did not produce an external_directory request", name)
		}
	}
}

// TestPendingPermEventsOldestFirst: replay rebuilds the approval dock, so the
// cards must come back in registration order.
func TestPendingPermEventsOldestFirst(t *testing.T) {
	r := &Runner{pendingPerms: map[string]*pendingPerm{}, doom: map[string]int{}}
	for _, id := range []string{"first", "second"} {
		r.registerPerm(permission.Request{Action: "bash", Resources: []string{id}, Tool: "Bash"})
	}
	events := r.PendingPermEvents()
	if len(events) != 2 {
		t.Fatalf("events = %d", len(events))
	}
	if events[0]["id"] != "p1" || events[1]["id"] != "p2" {
		t.Fatalf("order = %v, %v", events[0]["id"], events[1]["id"])
	}
	if events[0]["type"] != "perm.request" || events[0]["action"] != "bash" {
		t.Fatalf("shape = %+v", events[0])
	}
}
