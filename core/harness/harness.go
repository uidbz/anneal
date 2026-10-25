// Package harness mounts Anneal's editable harness components as files
// under ~/.anneal/harness (Harness²-style, arXiv 2610.14224). Components
// are re-read per run; missing/empty components mean "pure base harness".
//
//	<root>/system.md         appended to the system prompt (persona/workstyle)
//	<root>/guardrails.md     prose appended to the prompt + machine rule lines
//	                         ("deny: Bash(rm -rf *)", "ask: Write(...)",
//	                         "allow: Bash(go test *)") enforced as permission
//	                         rules at Runner creation (restart to apply)
//	<root>/tool-guidance.md  appended context on how to use Anneal's tools
//	<root>/subagents/*.md    dive subagent FileLoader format (description /
//	                         tools / disallowed-tools frontmatter + prompt
//	                         body); file definitions override built-ins
//	<root>/scripts/          helper scripts surfaced to the model as a
//	                         manifest (run via the bash tool)
package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/deepnoodle-ai/dive/subagent"
)

// Layer is one prompt-mounted component section.
type Layer struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Store reads the harness component tree rooted at dir.
type Store struct {
	root string
}

func New(dir string) *Store { return &Store{root: dir} }

func (s *Store) Root() string         { return s.root }
func (s *Store) subagentsDir() string { return filepath.Join(s.root, "subagents") }
func (s *Store) scriptsDir() string   { return filepath.Join(s.root, "scripts") }

const readme = `# Anneal harness components

Editable files mounted into every run (Harness²-style recursive harnessing).
Empty or missing = unchanged base behavior.

- system.md        appended to the system prompt (persona, workstyle rules)
- guardrails.md    prose appended to the prompt; lines of the form
                     deny: Bash(rm -rf *)
                     ask: Write(**/*.env)
                     allow: Bash(go test *)
                   are machine-enforced permission rules (applied at
                   startup; restart anneal after changing them)
- tool-guidance.md extra per-tool usage notes for the model
- subagents/       one .md per subagent: YAML frontmatter with
                     description: when to use me
                     tools: Read, Grep            (optional allowlist)
                     disallowed-tools: Write      (optional)
                   followed by the subagent's system prompt
- scripts/         executable helpers the model is told about (one line
                   each; first "# description" comment becomes the hint)

Keep each injected component under the configured char limit
(config.json harness.char_limit, default 6000); longer content truncates.
Agent-authored harness edits arrive through the improvement library's
promote flow (~/.anneal/harness-lib), never automatically.
`

// Seed creates the component tree and README stub; never clobbers.
func (s *Store) Seed() error {
	for _, d := range []string{s.root, s.subagentsDir(), s.scriptsDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	f := filepath.Join(s.root, "README.md")
	if _, err := os.Stat(f); err != nil {
		if err := os.WriteFile(f, []byte(readme), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) read(name string) string {
	b, err := os.ReadFile(filepath.Join(s.root, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// clip truncates to limit chars with a visible marker (limit <= 0 = no cap).
func clip(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	return s[:limit] + "\n… [truncated]"
}

// guardProse returns guardrails.md without its machine rule lines.
func guardProse(raw string) string {
	var keep []string
	for _, line := range strings.Split(raw, "\n") {
		if _, _, ok := guardRule(line); ok {
			continue
		}
		keep = append(keep, line)
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// guardRule parses "deny: Bash(rm *)" style lines.
func guardRule(line string) (kind, spec string, ok bool) {
	trimmed := strings.TrimSpace(line)
	colon := strings.Index(trimmed, ":")
	if colon <= 0 {
		return "", "", false
	}
	kind = strings.ToLower(strings.TrimSpace(trimmed[:colon]))
	if kind != "allow" && kind != "ask" && kind != "deny" {
		return "", "", false
	}
	// Reject markdown list/bold prefixes ("- deny: …", "**deny**: …").
	if strings.ContainsAny(trimmed[:colon], "*`#>[]") || strings.HasSuffix(trimmed[:colon-1], "-") {
		return "", "", false
	}
	spec = strings.TrimSpace(trimmed[colon+1:])
	if spec == "" {
		return "", "", false
	}
	return kind, spec, true
}

// GuardRules returns allow/ask/deny rule specs from guardrails.md, in the
// same string form as config agent.allow/ask/deny ("Bash" or "Bash(go *)").
func (s *Store) GuardRules() (allow, ask, deny []string) {
	raw := s.read("guardrails.md")
	for _, line := range strings.Split(raw, "\n") {
		kind, spec, ok := guardRule(line)
		if !ok {
			continue
		}
		switch kind {
		case "allow":
			allow = append(allow, spec)
		case "ask":
			ask = append(ask, spec)
		case "deny":
			deny = append(deny, spec)
		}
	}
	return allow, ask, deny
}

// ScriptsManifest renders the scripts/ component; "" when none.
func (s *Store) ScriptsManifest() string {
	entries, err := os.ReadDir(s.scriptsDir())
	if err != nil || len(entries) == 0 {
		return ""
	}
	var lines []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(s.scriptsDir(), e.Name())
		desc := ""
		if b, err := os.ReadFile(path); err == nil {
			if first := strings.SplitN(string(b), "\n", 2)[0]; strings.HasPrefix(first, "# ") {
				desc = " — " + strings.TrimSpace(first[2:])
			}
		}
		lines = append(lines, fmt.Sprintf("- %s%s", path, desc))
	}
	if len(lines) == 0 {
		return ""
	}
	sort.Strings(lines)
	return "Harness helper scripts (run with the bash tool):\n" + strings.Join(lines, "\n")
}

// Layers returns prompt-mounted components in mount order, clipped to
// limit chars each; missing/empty components are skipped.
func (s *Store) Layers(limit int) []Layer {
	var out []Layer
	add := func(name, content string) {
		if content == "" {
			return
		}
		out = append(out, Layer{Name: name, Content: clip(content, limit)})
	}
	add("system", s.read("system.md"))
	add("guardrails", guardProse(s.read("guardrails.md")))
	add("tool-guidance", s.read("tool-guidance.md"))
	add("scripts", s.ScriptsManifest())
	return out
}

// Subagents loads file-defined subagent definitions; notes carries
// per-file parse problems (never fatal).
func (s *Store) Subagents() (map[string]*subagent.Definition, []string) {
	defs, err := subagent.LoadFromDirectory(s.subagentsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{"subagents: " + err.Error()}
	}
	return defs, nil
}

var known = map[string]bool{
	"system.md": true, "guardrails.md": true, "tool-guidance.md": true,
	"README.md": true, "subagents": true, "scripts": true,
}

// Files lists editable component files (top-level .md + subagents/*.md),
// as slash-prefixed relative paths.
func (s *Store) Files() []string {
	var out []string
	entries, _ := os.ReadDir(s.root)
	for _, e := range entries {
		if e.IsDir() && e.Name() == "subagents" {
			sub, _ := os.ReadDir(filepath.Join(s.root, "subagents"))
			for _, x := range sub {
				if !x.IsDir() && strings.HasSuffix(x.Name(), ".md") {
					out = append(out, "subagents/"+x.Name())
				}
			}
			continue
		}
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && e.Name() != "README.md" {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// safeRel validates an editable component path relative to the root.
func (s *Store) safeRel(rel string) (string, error) {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") ||
		strings.Contains(rel, "\\") {
		return "", fmt.Errorf("harness: bad component path %q", rel)
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if !strings.HasSuffix(clean, ".md") {
		return "", fmt.Errorf("harness: only .md components are editable (%q)", rel)
	}
	if strings.Contains(clean, "/") && !strings.HasPrefix(clean, "subagents/") {
		return "", fmt.Errorf("harness: bad component path %q", rel)
	}
	return filepath.Join(s.root, clean), nil
}

// GetFile reads one component file.
func (s *Store) GetFile(rel string) (string, error) {
	p, err := s.safeRel(rel)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// SaveFile writes one component file (prose layers re-read next run;
// guardrail RULES need a restart).
func (s *Store) SaveFile(rel, content string) error {
	p, err := s.safeRel(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// LayersFromDir renders any directory of .md component files as prompt
// layers (candidate/successor harnesses carry the same layout).
func LayersFromDir(dir string, limit int) []Layer {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && e.Name() != "README.md" && e.Name() != "decisions.md" {
			names = append(names, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	sort.Strings(names)
	var out []Layer
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n+".md"))
		if err != nil {
			continue
		}
		c := strings.TrimSpace(string(b))
		if c == "" {
			continue
		}
		out = append(out, Layer{Name: n, Content: clip(c, limit)})
	}
	return out
}

// Validate returns human-readable warnings (bad files, oversize, unknowns).
func (s *Store) Validate(limit int) []string {
	var warns []string
	if _, notes := s.Subagents(); len(notes) > 0 {
		warns = append(warns, notes...)
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return []string{"harness dir unreadable: " + err.Error()}
	}
	for _, e := range entries {
		if !known[e.Name()] {
			warns = append(warns, fmt.Sprintf("harness: ignored unknown entry %q", e.Name()))
			continue
		}
		if e.IsDir() {
			continue
		}
		content := s.read(e.Name())
		if limit > 0 && len(content) > limit {
			warns = append(warns, fmt.Sprintf("harness: %s is %d chars (> char_limit %d; will truncate)", e.Name(), len(content), limit))
		}
	}
	return warns
}
