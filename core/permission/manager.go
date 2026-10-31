package permission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/uidbz/anneal/core/fsutil"
)

// Options configures a Manager.
//
// Base is the built-in/legacy layer (allow/ask/deny lists, the secret-file
// guard). Rules is the user/harness layer: any rule there wins over Base,
// which is what lets an explicit "ask" keep prompting even for commands the
// read-only classifier would otherwise allow. For compatibility, a caller
// that only sets Rules gets it as Base with no overrides.
type Options struct {
	Mode         string
	Base         Rules
	Rules        Rules
	Project      string // absolute project root; saved rules are scoped to it
	SavedPath    string // JSON file for "always" grants ("" = memory only)
	ReadOnlyBash bool   // auto-allow classified read-only Bash commands
}

// Manager evaluates permission requests and remembers "always" grants.
// Rule evaluation and mode policy mirror OpenCode: configured deny rules are
// absolute, then the request is evaluated (last match wins), then the mode
// layer turns asks into allow/deny where appropriate.
type Manager struct {
	mu           sync.RWMutex
	persistMu    sync.Mutex
	mode         Mode
	base         Rules
	override     Rules
	session      Rules // "allow for this session" grants (memory only)
	saved        Rules
	project      string
	savedPath    string
	readonlyBash bool
}

// NewManager creates a manager and loads any saved project grants.
func NewManager(opts Options) *Manager {
	mode := Mode(opts.Mode)
	if !ValidMode(string(mode)) {
		mode = ModeDefault
	}
	base, over := opts.Base, opts.Rules
	if len(base) == 0 && len(over) > 0 {
		base, over = over, nil
	}
	m := &Manager{
		mode:         mode,
		base:         NormalizeRules(base),
		override:     NormalizeRules(over),
		project:      canonicalProject(opts.Project),
		savedPath:    opts.SavedPath,
		readonlyBash: opts.ReadOnlyBash,
	}
	m.load()
	return m
}

// Mode returns the active mode.
func (m *Manager) Mode() Mode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.mode
}

// SetMode changes the mode. Invalid modes are ignored.
func (m *Manager) SetMode(mode string) {
	if !ValidMode(mode) {
		return
	}
	m.mu.Lock()
	m.mode = Mode(mode)
	m.mu.Unlock()
}

// NormalizeRules canonicalizes actions and coerces unknown effects to ask.
func NormalizeRules(in Rules) Rules {
	out := make(Rules, 0, len(in))
	for _, rule := range in {
		out = append(out, normalizeRule(rule))
	}
	return out
}

// Rules returns a copy of the configured rules (base then overrides).
func (m *Manager) Rules() Rules {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := append(Rules(nil), m.base...)
	return append(out, m.override...)
}

// Saved returns a copy of the "always" grants for this project.
func (m *Manager) Saved() Rules {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append(Rules(nil), m.saved...)
}

// Evaluate applies configured rules, saved grants, and the mode layer.
//
// Order: configured deny rules are absolute (neither saved grants nor bypass
// mode override them). Then per resource, a user/harness override wins over
// the base layer. Read-only Bash is auto-allowed only while the base layer is
// what asked: an explicit override "ask" keeps prompting.
func (m *Manager) Evaluate(req Request) Result {
	resources := req.Resources
	if len(resources) == 0 {
		resources = []string{DefaultResource}
	}

	m.mu.RLock()
	mode := m.mode
	base := append(Rules(nil), m.base...)
	over := append(Rules(nil), m.override...)
	session := append(Rules(nil), m.session...)
	saved := append(Rules(nil), m.saved...)
	readonly := m.readonlyBash
	m.mu.RUnlock()

	baseRules := append(append(append(Rules(nil), base...), session...), saved...)
	overRules := append(append(append(Rules(nil), over...), session...), saved...)
	relevant := relevantRules(req.Action, append(append(Rules(nil), base...), over...))

	// Configured deny rules are absolute: a saved "always" grant, a session
	// grant or bypass mode cannot override them (OpenCode's denied() check).
	for _, resource := range resources {
		if Evaluate(req.Action, resource, append(append(Rules(nil), base...), over...)).Effect == Deny {
			return Result{Effect: Deny, Rules: relevant, Reason: "denied by permission rule"}
		}
	}

	// Plan mode is a hard read-only gate.
	if mode == ModePlan && !req.ReadOnly {
		return Result{
			Effect: Deny,
			Rules:  relevant,
			Reason: "plan mode allows only read-only tools",
		}
	}

	effect := Allow
	explicitAsk := false
	for _, resource := range resources {
		if e, matched := evaluateMatch(req.Action, resource, overRules); matched {
			switch e {
			case Deny:
				return Result{Effect: Deny, Rules: relevant, Reason: "denied by permission rule"}
			case Allow:
			default:
				effect, explicitAsk = Ask, true
			}
			continue
		}
		switch Evaluate(req.Action, resource, baseRules).Effect {
		case Deny:
			return Result{Effect: Deny, Rules: relevant, Reason: "denied by permission rule"}
		case Allow:
		default:
			effect = Ask
		}
	}

	if effect == Ask && readonly && req.ReadOnly && req.Action == "bash" && !explicitAsk {
		effect = Allow
	}

	switch effect {
	case Ask:
		switch mode {
		case ModeBypassPermissions:
			effect = Allow
		case ModeDontAsk:
			return Result{Effect: Deny, Rules: relevant, Reason: "tool not explicitly allowed (dontAsk mode)"}
		case ModeAcceptEdits:
			if req.Action == "edit" {
				effect = Allow
			}
		}
	}

	return Result{Effect: effect, Rules: relevant}
}

// evaluateMatch returns the last rule matching action/resource and whether one
// matched at all (an implicit "ask" and an explicit "ask" differ for the
// read-only Bash layer).
func evaluateMatch(action, resource string, rules Rules) (Effect, bool) {
	for i := len(rules) - 1; i >= 0; i-- {
		rule := rules[i]
		if !Match(rule.Action, action) || !Match(rule.Resource, resource) {
			continue
		}
		effect := rule.Effect
		if effect != Allow && effect != Deny && effect != Ask {
			effect = Ask
		}
		return effect, true
	}
	return Ask, false
}

func relevantRules(action string, rules Rules) Rules {
	var out Rules
	for _, rule := range rules {
		if Match(rule.Action, action) {
			out = append(out, rule)
		}
	}
	return out
}

// AddSaved records an "always" grant for action, one rule per pattern, and
// persists it. Grants are project-scoped. Invalid or empty patterns are
// ignored.
func (m *Manager) AddSaved(action string, patterns []string) error {
	action = CanonicalAction(action)
	var added []Rule
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		added = append(added, Rule{
			Action:   action,
			Resource: ExpandHome(pattern),
			Effect:   Allow,
		})
	}
	if len(added) == 0 {
		return nil
	}

	m.mu.Lock()
	for _, rule := range added {
		duplicate := false
		for _, existing := range m.saved {
			if existing.Action == rule.Action && existing.Resource == rule.Resource {
				duplicate = true
				break
			}
		}
		if !duplicate {
			m.saved = append(m.saved, rule)
		}
	}
	m.mu.Unlock()
	return m.persist()
}

// AddSession records an in-memory grant for the rest of this session. It is
// not persisted and is cleared when the session changes.
func (m *Manager) AddSession(action string, patterns []string) {
	action = CanonicalAction(action)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		m.session = append(m.session, Rule{
			Action: action, Resource: ExpandHome(pattern), Effect: Allow,
		})
	}
}

// ClearSession drops session-scoped grants (new/opened session).
func (m *Manager) ClearSession() {
	m.mu.Lock()
	m.session = nil
	m.mu.Unlock()
}

// RemoveSaved deletes one project grant. It is a no-op when no such rule
// exists (the UI may race another window's removal).
func (m *Manager) RemoveSaved(action, resource string) error {
	action = CanonicalAction(action)
	resource = ExpandHome(strings.TrimSpace(resource))
	m.mu.Lock()
	kept := m.saved[:0]
	for _, rule := range m.saved {
		if rule.Action == action && rule.Resource == resource {
			continue
		}
		kept = append(kept, rule)
	}
	m.saved = kept
	m.mu.Unlock()
	return m.persist()
}

// ClearSaved removes all saved grants for the project.
func (m *Manager) ClearSaved() error {
	m.mu.Lock()
	m.saved = nil
	m.mu.Unlock()
	return m.persist()
}

type savedFile struct {
	Projects map[string][]Rule `json:"projects"`
}

func (m *Manager) load() {
	if m.savedPath == "" || m.project == "" {
		return
	}
	data, err := os.ReadFile(m.savedPath)
	if err != nil {
		return
	}
	var doc savedFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return
	}
	for _, rule := range doc.Projects[m.project] {
		if rule.Effect != Allow {
			continue
		}
		rule.Action = CanonicalAction(rule.Action)
		if rule.Resource == "" {
			rule.Resource = DefaultResource
		}
		if malformedSaved(rule) {
			fmt.Fprintf(os.Stderr, "permission: dropped malformed saved grant %s %q\n",
				rule.Action, rule.Resource)
			continue
		}
		m.saved = append(m.saved, rule)
	}
}

// malformedSaved recognizes grants produced by old parser bugs: the numeric
// fragment of `2>&1` (saved as "bash 1 *") and outside-directory resources
// containing a split substitution (`/go/pkg/mod)/x/*`).
func malformedSaved(rule Rule) bool {
	if rule.Action == "bash" && strings.HasSuffix(rule.Resource, " *") {
		if _, err := strconv.Atoi(strings.TrimSuffix(rule.Resource, " *")); err == nil {
			return true
		}
	}
	if rule.Action == "external_directory" && strings.Contains(rule.Resource, ")/") {
		return true
	}
	return false
}

func (m *Manager) persist() error {
	if m.savedPath == "" || m.project == "" {
		return nil
	}
	m.persistMu.Lock()
	defer m.persistMu.Unlock()

	// Read-modify-write under an inter-process lock so two local windows
	// sharing ~/.anneal cannot drop each other's grants.
	return fsutil.WithFileLock(m.savedPath, func() error {
		doc := savedFile{Projects: map[string][]Rule{}}
		if data, err := os.ReadFile(m.savedPath); err == nil {
			_ = json.Unmarshal(data, &doc)
		}
		if doc.Projects == nil {
			doc.Projects = map[string][]Rule{}
		}

		m.mu.RLock()
		rules := append([]Rule(nil), m.saved...)
		m.mu.RUnlock()
		if len(rules) == 0 {
			delete(doc.Projects, m.project)
		} else {
			doc.Projects[m.project] = rules
		}

		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		return fsutil.WriteFileAtomic(m.savedPath, data, 0o600)
	})
}

func canonicalProject(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs)
}
