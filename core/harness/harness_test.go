package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s := New(filepath.Join(t.TempDir(), "harness"))
	if err := s.Seed(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFilesAndEdit(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(filepath.Join(s.Root(), "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(s.Root(), "subagents", "auditor.md"), []byte("# audit"), 0o644)
	os.WriteFile(filepath.Join(s.Root(), "system.md"), []byte("base"), 0o644)
	fs := s.Files()
	joined := strings.Join(fs, ",")
	if !strings.Contains(joined, "subagents/auditor.md") || !strings.Contains(joined, "system.md") {
		t.Fatalf("files: %v", fs)
	}
	if strings.Contains(joined, "README") {
		t.Fatalf("README editable: %v", fs)
	}
	for _, bad := range []string{"../evil.md", "/abs.md", "scripts/x.md", "x.txt",
		"a/../b.md", `a\b.md`, ""} {
		if _, err := s.GetFile(bad); err == nil {
			t.Fatalf("jail accepted get %q", bad)
		}
		if err := s.SaveFile(bad, "x"); err == nil {
			t.Fatalf("jail accepted save %q", bad)
		}
	}
	if err := s.SaveFile("subagents/auditor.md", "# changed"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFile("subagents/auditor.md")
	if err != nil || got != "# changed" {
		t.Fatalf("roundtrip: %q %v", got, err)
	}
}

func TestSeedIdempotent(t *testing.T) {
	s := newStore(t)
	write := filepath.Join(s.Root(), "system.md")
	os.WriteFile(write, []byte("keep me"), 0o644)
	if err := s.Seed(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(write)
	if string(b) != "keep me" {
		t.Fatal("Seed clobbered existing file")
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "README.md")); err != nil {
		t.Fatal("no README stub")
	}
}

func TestLayers(t *testing.T) {
	s := newStore(t)
	os.WriteFile(filepath.Join(s.Root(), "system.md"), []byte("be terse\n"), 0o644)
	os.WriteFile(filepath.Join(s.Root(), "guardrails.md"),
		[]byte("never force-push\ndeny: Bash(git push --force*)\n"), 0o644)
	os.WriteFile(filepath.Join(s.Root(), "tool-guidance.md"),
		[]byte(strings.Repeat("x", 100)), 0o644)

	ls := s.Layers(60)
	var names []string
	for _, l := range ls {
		names = append(names, l.Name)
	}
	want := "system guardrails tool-guidance"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("layer order: %q want %q", got, want)
	}
	for _, l := range ls {
		if l.Name == "guardrails" && strings.Contains(l.Content, "deny:") {
			t.Fatal("rule line leaked into prose layer")
		}
		if l.Name == "guardrails" && !strings.Contains(l.Content, "never force-push") {
			t.Fatal("prose lost")
		}
		if l.Name == "tool-guidance" && !strings.HasSuffix(l.Content, "[truncated]") {
			t.Fatalf("not clipped: %d chars", len(l.Content))
		}
	}
}

func TestGuardRules(t *testing.T) {
	s := newStore(t)
	os.WriteFile(filepath.Join(s.Root(), "guardrails.md"), []byte(`
prose line here
allow: Bash(go test *)
ask: Write(**/.env)
deny: Bash(rm -rf *)
- deny: not-a-rule (markdown list)
**deny**: not-a-rule (bold)
deny: empty-spec-ok? no spec below
deny:
`), 0o644)
	allow, ask, deny := s.GuardRules()
	if len(allow) != 1 || allow[0] != "Bash(go test *)" {
		t.Fatalf("allow: %v", allow)
	}
	if len(ask) != 1 || ask[0] != "Write(**/.env)" {
		t.Fatalf("ask: %v", ask)
	}
	// "deny: empty-spec-ok? no spec below" IS a rule (spec = "empty-spec-ok? ...").
	// The trailing bare "deny:" has empty spec and must not count.
	if len(deny) != 2 || deny[0] != "Bash(rm -rf *)" {
		t.Fatalf("deny: %v", deny)
	}
}

func TestScriptsManifest(t *testing.T) {
	s := newStore(t)
	if m := s.ScriptsManifest(); m != "" {
		t.Fatalf("empty dir should give empty manifest, got %q", m)
	}
	os.WriteFile(filepath.Join(s.scriptsDir(), "coverage.sh"),
		[]byte("# run tests with coverage\ngo test -cover ./...\n"), 0o755)
	m := s.ScriptsManifest()
	if !strings.Contains(m, "coverage.sh") || !strings.Contains(m, "run tests with coverage") {
		t.Fatalf("manifest: %q", m)
	}
}

func TestSubagentsAndValidate(t *testing.T) {
	s := newStore(t)
	os.WriteFile(filepath.Join(s.subagentsDir(), "summarizer.md"), []byte(`---
description: summarize files in one line
tools: [Read, Grep]
---
You summarize. One line only.
`), 0o644)
	defs, notes := s.Subagents()
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
	d, ok := defs["summarizer"]
	if !ok {
		t.Fatalf("defs: %v", defs)
	}
	if d.Description == "" || !strings.Contains(d.Prompt, "One line only") {
		t.Fatalf("def parsed wrong: %+v", d)
	}
	os.WriteFile(filepath.Join(s.Root(), "system.md"), []byte(strings.Repeat("y", 7000)), 0o644)
	os.WriteFile(filepath.Join(s.Root(), "NOTES.txt"), []byte("hi"), 0o644)
	warns := s.Validate(6000)
	var hasUnknown, hasOversize bool
	for _, w := range warns {
		if strings.Contains(w, "NOTES.txt") {
			hasUnknown = true
		}
		if strings.Contains(w, "system.md") && strings.Contains(w, "truncate") {
			hasOversize = true
		}
	}
	if !hasUnknown || !hasOversize {
		t.Fatalf("warnings: %v", warns)
	}
}
