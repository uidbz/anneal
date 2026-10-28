package permission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchWildcards(t *testing.T) {
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"*", "anything/at all", true},
		{"git *", "git status", true},
		{"git *", "git status --porcelain", true},
		{"git status *", "git status", true},
		{"git status *", "git status --porcelain", true},
		{"git status *", "git stash", false},
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/deep/main.go", true}, // * crosses slashes, like OpenCode
		{"src/?.go", "src/a.go", true},
		{"src/?.go", "src/ab.go", false},
		{"*.env", "config/.env", true},
		{"*.env.example", "config/.env.example", true},
		{"~/projects/*", "/home/user/projects/a", false}, // patterns are expanded by the parser
		{`C:\src\*`, "C:/src/a.go", true},
		{"Bash", "bash", false}, // actions are case-sensitive once canonicalized
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.value); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.value, got, c.want)
		}
	}
}

func TestParseLegacyRules(t *testing.T) {
	cases := []struct {
		spec   string
		action string
		shared string
	}{
		{"Bash", "bash", "*"},
		{"Bash(go test *)", "bash", "go test *"},
		{"Read(**/*.env)", "read", "**/*.env"},
		{"Write(src/*)", "edit", "src/*"},
		{"Edit(src/*)", "edit", "src/*"},
		{"ListDirectory", "read", "*"},
		{"Skill", "skill", "*"},
		{"Agent", "task", "*"},
		{"todo", "todowrite", "*"},
		{"script", "script", "*"},
		{"mcp__gortex__explore", "mcp__gortex__explore", "*"},
	}
	for _, c := range cases {
		rule := Parse(Allow, c.spec)
		if rule.Action != c.action || rule.Resource != c.shared || rule.Effect != Allow {
			t.Errorf("Parse(%q) = %+v, want %s %s allow", c.spec, rule, c.action, c.shared)
		}
	}
}

func TestConfigUnmarshalPreservesOrder(t *testing.T) {
	doc := `{
	  "bash": {"*": "ask", "git *": "allow", "rm *": "deny", "git push *": "deny"},
	  "edit": "allow",
	  "read": {"*.env": "ask", "*.env.*": "ask", "*.env.example": "allow"},
	  "mcp__gortex__explore": "allow"
	}`
	var cfg Config
	if err := json.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatal(err)
	}
	want := []Rule{
		{Action: "bash", Resource: "*", Effect: Ask},
		{Action: "bash", Resource: "git *", Effect: Allow},
		{Action: "bash", Resource: "rm *", Effect: Deny},
		{Action: "bash", Resource: "git push *", Effect: Deny},
		{Action: "edit", Resource: "*", Effect: Allow},
		{Action: "read", Resource: "*.env", Effect: Ask},
		{Action: "read", Resource: "*.env.*", Effect: Ask},
		{Action: "read", Resource: "*.env.example", Effect: Allow},
		{Action: "mcp__gortex__explore", Resource: "*", Effect: Allow},
	}
	if len(cfg) != len(want) {
		t.Fatalf("got %d rules, want %d: %+v", len(cfg), len(want), cfg)
	}
	for i := range want {
		if cfg[i] != want[i] {
			t.Errorf("rule %d = %+v, want %+v", i, cfg[i], want[i])
		}
	}

	// Last match wins, including a more specific rule after a broad one.
	if got := Evaluate("bash", "git push origin", Rules(cfg)); got.Effect != Deny {
		t.Errorf("git push: %+v", got)
	}
	if got := Evaluate("bash", "git status", Rules(cfg)); got.Effect != Allow {
		t.Errorf("git status: %+v", got)
	}
	if got := Evaluate("bash", "rm -rf /", Rules(cfg)); got.Effect != Deny {
		t.Errorf("rm: %+v", got)
	}
	if got := Evaluate("bash", "ls", Rules(cfg)); got.Effect != Ask {
		t.Errorf("ls: %+v", got)
	}
	if got := Evaluate("read", "config/.env", Rules(cfg)); got.Effect != Ask {
		t.Errorf(".env: %+v", got)
	}
	if got := Evaluate("read", "config/.env.example", Rules(cfg)); got.Effect != Allow {
		t.Errorf(".env.example: %+v", got)
	}
}

func TestConfigStringAndArrayShapes(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`"deny"`), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg) != 1 || cfg[0] != (Rule{Action: "*", Resource: "*", Effect: Deny}) {
		t.Fatalf("string shape: %+v", cfg)
	}

	cfg = nil
	if err := json.Unmarshal([]byte(`[{"action":"Bash","resource":"go test *","effect":"allow"}]`), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg) != 1 || cfg[0].Action != "bash" || cfg[0].Resource != "go test *" || cfg[0].Effect != Allow {
		t.Fatalf("array shape: %+v", cfg)
	}
}

func TestConfigMarshalObjectShape(t *testing.T) {
	cfg := Config{
		{Action: "bash", Resource: "*", Effect: Ask},
		{Action: "bash", Resource: "git *", Effect: Allow},
		{Action: "edit", Resource: "*", Effect: Allow},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, `{"bash":{`) || !strings.Contains(text, `"edit":"allow"`) {
		t.Fatalf("unexpected shape: %s", text)
	}

	var back Config
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != len(cfg) {
		t.Fatalf("roundtrip lost rules: %+v", back)
	}
	for i := range cfg {
		if back[i] != cfg[i] {
			t.Errorf("rule %d roundtrip = %+v, want %+v", i, back[i], cfg[i])
		}
	}
}

func TestDecideCompoundCommands(t *testing.T) {
	rules := Rules{
		{Action: "bash", Resource: "git *", Effect: Allow},
		{Action: "bash", Resource: "rm *", Effect: Deny},
	}
	if effect, _ := Decide("bash", []string{"git status", "rm -rf /"}, rules); effect != Deny {
		t.Fatalf("deny should win: %s", effect)
	}
	if effect, _ := Decide("bash", []string{"git status", "git diff"}, rules); effect != Allow {
		t.Fatalf("all allowed should allow: %s", effect)
	}
	if effect, _ := Decide("bash", []string{"git status", "make test"}, rules); effect != Ask {
		t.Fatalf("unmatched segment should ask: %s", effect)
	}
}

func TestManagerSavedRules(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	savedPath := filepath.Join(dir, "permissions.json")

	m := NewManager(Options{
		Rules:     Rules{{Action: "bash", Resource: "*", Effect: Ask}},
		Project:   project,
		SavedPath: savedPath,
	})
	if got := m.Evaluate(Request{Action: "bash", Resources: []string{"git status"}}).Effect; got != Ask {
		t.Fatalf("before save: %s", got)
	}
	if err := m.AddSaved("bash", []string{"git status *"}); err != nil {
		t.Fatal(err)
	}
	if got := m.Evaluate(Request{Action: "bash", Resources: []string{"git status --short"}}).Effect; got != Allow {
		t.Fatalf("saved prefix: %s", got)
	}
	if got := m.Evaluate(Request{Action: "bash", Resources: []string{"git diff"}}).Effect; got != Ask {
		t.Fatalf("unrelated command: %s", got)
	}

	// Reloading the same project keeps the grant; another project does not.
	reloaded := NewManager(Options{
		Rules:     Rules{{Action: "bash", Resource: "*", Effect: Ask}},
		Project:   project,
		SavedPath: savedPath,
	})
	if got := reloaded.Evaluate(Request{Action: "bash", Resources: []string{"git status"}}).Effect; got != Allow {
		t.Fatalf("reloaded grant: %s", got)
	}
	other := NewManager(Options{
		Rules:     Rules{{Action: "bash", Resource: "*", Effect: Ask}},
		Project:   filepath.Join(dir, "other"),
		SavedPath: savedPath,
	})
	if got := other.Evaluate(Request{Action: "bash", Resources: []string{"git status"}}).Effect; got != Ask {
		t.Fatalf("project scoping: %s", got)
	}
}

func TestManagerDenyIsAbsolute(t *testing.T) {
	m := NewManager(Options{
		Rules: Rules{
			{Action: "bash", Resource: "*", Effect: Allow},
			{Action: "bash", Resource: "rm *", Effect: Deny},
		},
	})
	if err := m.AddSaved("bash", []string{"rm -rf *"}); err != nil {
		t.Fatal(err)
	}
	if got := m.Evaluate(Request{Action: "bash", Resources: []string{"rm -rf /"}}).Effect; got != Deny {
		t.Fatalf("saved allow must not override configured deny: %s", got)
	}
}

func TestManagerModes(t *testing.T) {
	rules := Rules{
		{Action: "bash", Resource: "*", Effect: Ask},
		{Action: "edit", Resource: "*", Effect: Ask},
		{Action: "read", Resource: "*", Effect: Allow},
	}
	m := NewManager(Options{Mode: string(ModeBypassPermissions), Rules: rules})
	if got := m.Evaluate(Request{Action: "bash", Resources: []string{"ls"}}).Effect; got != Allow {
		t.Fatalf("bypass: %s", got)
	}

	m.SetMode(string(ModeDontAsk))
	if got := m.Evaluate(Request{Action: "bash", Resources: []string{"ls"}}).Effect; got != Deny {
		t.Fatalf("dontAsk: %s", got)
	}

	m.SetMode(string(ModeAcceptEdits))
	if got := m.Evaluate(Request{Action: "edit", Resources: []string{"a.go"}}).Effect; got != Allow {
		t.Fatalf("acceptEdits edit: %s", got)
	}
	if got := m.Evaluate(Request{Action: "bash", Resources: []string{"ls"}}).Effect; got != Ask {
		t.Fatalf("acceptEdits bash: %s", got)
	}

	m.SetMode(string(ModePlan))
	if got := m.Evaluate(Request{Action: "edit", Resources: []string{"a.go"}}).Effect; got != Deny {
		t.Fatalf("plan edit: %s", got)
	}
	if got := m.Evaluate(Request{Action: "read", Resources: []string{"a.go"}, ReadOnly: true}).Effect; got != Allow {
		t.Fatalf("plan read: %s", got)
	}
}
