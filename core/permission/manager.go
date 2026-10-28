package permission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/uidbz/anneal/core/fsutil"
)

// Options configures a Manager.
type Options struct {
	Mode      string
	Rules     Rules
	Project   string // absolute project root; saved rules are scoped to it
	SavedPath string // JSON file for "always" grants ("" = memory only)
}

// Manager evaluates permission requests and remembers "always" grants.
// Rule evaluation and mode policy mirror OpenCode: configured deny rules are
// absolute, then the request is evaluated (last match wins), then the mode
// layer turns asks into allow/deny where appropriate.
type Manager struct {
	mu        sync.RWMutex
	persistMu sync.Mutex
	mode      Mode
	rules     Rules
	saved     Rules
	project   string
	savedPath string
}

// NewManager creates a manager and loads any saved project grants.
func NewManager(opts Options) *Manager {
	mode := Mode(opts.Mode)
	if !ValidMode(string(mode)) {
		mode = ModeDefault
	}
	m := &Manager{
		mode:      mode,
		rules:     NormalizeRules(opts.Rules),
		project:   canonicalProject(opts.Project),
		savedPath: opts.SavedPath,
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

// Rules returns a copy of the configured rules.
func (m *Manager) Rules() Rules {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append(Rules(nil), m.rules...)
}

// Saved returns a copy of the "always" grants for this project.
func (m *Manager) Saved() Rules {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append(Rules(nil), m.saved...)
}

// Evaluate applies configured rules, saved grants, and the mode layer.
func (m *Manager) Evaluate(req Request) Result {
	resources := req.Resources
	if len(resources) == 0 {
		resources = []string{DefaultResource}
	}

	m.mu.RLock()
	mode := m.mode
	configured := append(Rules(nil), m.rules...)
	saved := append(Rules(nil), m.saved...)
	m.mu.RUnlock()

	// Configured deny rules are absolute: a saved "always" grant or bypass
	// mode cannot override them (OpenCode's denied() check).
	for _, resource := range resources {
		if Evaluate(req.Action, resource, configured).Effect == Deny {
			return Result{
				Effect: Deny,
				Rules:  relevantRules(req.Action, configured),
				Reason: "denied by permission rule",
			}
		}
	}

	// Plan mode is a hard read-only gate.
	if mode == ModePlan && !req.ReadOnly {
		return Result{
			Effect: Deny,
			Rules:  relevantRules(req.Action, configured),
			Reason: "plan mode allows only read-only tools",
		}
	}

	all := append(append(Rules(nil), configured...), saved...)
	effect, relevant := Decide(req.Action, resources, all)

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
		m.saved = append(m.saved, rule)
	}
}

func (m *Manager) persist() error {
	if m.savedPath == "" || m.project == "" {
		return nil
	}
	m.persistMu.Lock()
	defer m.persistMu.Unlock()

	// Read-modify-write so other projects' grants survive.
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
