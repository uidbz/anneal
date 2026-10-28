// Package permission implements an OpenCode-style permission system.
//
// A rule permits, asks about, or denies an action (a tool kind such as read,
// edit, bash, or an MCP tool name) applied to a resource (a path, shell
// command, URL, subagent type, ...). Rules are evaluated first-match from the
// end: the last matching rule wins, and the default is "ask".
//
// Configuration is deliberately close to OpenCode's:
//
//	"permission": {
//	  "bash": { "*": "ask", "git *": "allow", "rm *": "deny" },
//	  "edit": { "*": "deny", "docs/*.md": "allow" },
//	  "webfetch": "allow"
//	}
//
// Go maps do not preserve order, so Config decodes JSON tokens directly:
// entries keep the order the author wrote them in, which is what makes
// last-match-wins useful.
package permission

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Effect is what a rule does when it matches.
type Effect string

const (
	Allow Effect = "allow"
	Ask   Effect = "ask"
	Deny  Effect = "deny"
)

// Outcome is the user's answer to an approval prompt. It mirrors OpenCode's
// once | always | reject replies.
type Outcome string

const (
	Once   Outcome = "once"
	Always Outcome = "always"
	Reject Outcome = "reject"
)

// Mode controls the global policy layer applied after rule evaluation.
type Mode string

const (
	ModeDefault           Mode = "default"
	ModePlan              Mode = "plan"
	ModeAcceptEdits       Mode = "acceptEdits"
	ModeBypassPermissions Mode = "bypassPermissions"
	ModeDontAsk           Mode = "dontAsk"
)

// ValidMode reports whether s is a supported permission mode.
func ValidMode(s string) bool {
	switch Mode(s) {
	case ModeDefault, ModePlan, ModeAcceptEdits, ModeBypassPermissions, ModeDontAsk:
		return true
	}
	return false
}

// Rule is one permission decision for an action/resource pair.
type Rule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   Effect `json:"effect"`
}

// Rules is an ordered ruleset; later entries take precedence.
type Rules []Rule

// Config is the user-facing form of a ruleset. It decodes OpenCode's string,
// object, and native array shapes, and encodes the compact object shape.
type Config []Rule

// Request is one permission check for a tool call.
type Request struct {
	// Action is the permission kind: read, edit, bash, webfetch, todowrite,
	// skill, task, external_directory, doom_loop, an MCP tool name, ...
	Action string
	// Resources are the concrete things being acted on. A compound bash
	// command contributes one resource per parsed command.
	Resources []string
	// Save lists the patterns an "always" reply should grant, mirroring
	// OpenCode's request.save field (for bash: safe command prefixes).
	Save []string
	// Tool is the original tool name, for prompts and UI labels.
	Tool string
	// Message is an optional human-readable explanation.
	Message string
	// Preview is a short JSON preview of the tool input, for prompts.
	Preview string
	// ReadOnly reports whether the underlying operation only reads.
	ReadOnly bool
}

// Result is the outcome of evaluating a request.
type Result struct {
	Effect Effect
	Rules  Rules // action-relevant rules, for diagnostics and prompts
	Reason string
}

// DefaultResource is used when a request carries no resources.
const DefaultResource = "*"

// CanonicalAction maps tool names and OpenCode action aliases to the action
// used by rules. Unknown names are lowercased so config keys are predictable.
func CanonicalAction(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "*"
	}
	if name == "*" {
		return "*"
	}
	if canonical, ok := actionAliases[strings.ToLower(name)]; ok {
		return canonical
	}
	return strings.ToLower(name)
}

var actionAliases = map[string]string{
	// shell
	"bash": "bash", "command": "bash", "shell": "bash", "exec": "bash",
	// reads
	"read": "read", "readfile": "read", "read_file": "read", "fileread": "read",
	"list": "read", "listdirectory": "read", "list_directory": "read", "ls": "read",
	// writes
	"edit": "edit", "write": "edit", "writefile": "edit", "write_file": "edit",
	"editfile": "edit", "edit_file": "edit", "patch": "edit", "apply_patch": "edit",
	"texteditor": "edit", "text_editor": "edit",
	// search
	"glob": "glob", "find": "glob",
	"grep": "grep", "rg": "grep",
	// network
	"webfetch": "webfetch", "fetch": "webfetch", "web_fetch": "webfetch",
	"websearch": "websearch", "web_search": "websearch",
	// agent meta
	"todo": "todowrite", "todowrite": "todowrite", "todo_write": "todowrite",
	"skill": "skill",
	"agent": "task", "task": "task", "subagent": "task",
	"externaldirectory": "external_directory", "external_directory": "external_directory",
	"doomloop": "doom_loop", "doom_loop": "doom_loop",
	"question": "question",
}

// EnvReadRules is the built-in secret-file guard: reads are allowed by the
// legacy defaults, but .env files ask and .env.example stays readable. It is
// applied before user rules, so an explicit later rule can override it.
func EnvReadRules() Rules {
	return Rules{
		{Action: "read", Resource: "*.env", Effect: Ask},
		{Action: "read", Resource: "*.env.*", Effect: Ask},
		{Action: "read", Resource: "*.env.example", Effect: Allow},
	}
}

// ParseRuleString parses anneal's legacy "Tool" / "Tool(specifier)" form, also
// used by harness guardrail lines. The tool name is canonicalized, so
// "Write(src/*)" becomes action "edit".
func ParseRuleString(effect string, spec string) Rule {
	return Parse(Effect(strings.ToLower(strings.TrimSpace(effect))), spec)
}

// Parse parses one legacy rule string.
func Parse(effect Effect, spec string) Rule {
	spec = strings.TrimSpace(spec)
	action, resource := spec, DefaultResource
	if i := strings.Index(spec, "("); i > 0 && strings.HasSuffix(spec, ")") {
		action = strings.TrimSpace(spec[:i])
		resource = strings.TrimSpace(spec[i+1 : len(spec)-1])
		if resource == "" {
			resource = DefaultResource
		}
	}
	return Rule{Action: CanonicalAction(action), Resource: ExpandHome(resource), Effect: effect}
}

// ParseRules parses several legacy rule strings with one effect.
func ParseRules(effect Effect, specs []string) Rules {
	out := make(Rules, 0, len(specs))
	for _, s := range specs {
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, Parse(effect, s))
	}
	return out
}

// Evaluate returns the last rule matching action/resource, or an implicit ask.
// Rulesets are searched in reverse order, so a later ruleset overrides an
// earlier one (OpenCode's merge order).
func Evaluate(action, resource string, rulesets ...Rules) Rule {
	if resource == "" {
		resource = DefaultResource
	}
	if action == "" {
		action = DefaultResource
	}
	for i := len(rulesets) - 1; i >= 0; i-- {
		set := rulesets[i]
		for j := len(set) - 1; j >= 0; j-- {
			rule := set[j]
			if Match(rule.Action, action) && Match(rule.Resource, resource) {
				effect := rule.Effect
				if effect != Allow && effect != Deny && effect != Ask {
					effect = Ask
				}
				return Rule{Action: action, Resource: resource, Effect: effect}
			}
		}
	}
	return Rule{Action: action, Resource: resource, Effect: Ask}
}

// Decide evaluates a whole request. Any denied resource denies the request;
// otherwise any asking resource asks. It also returns the rules relevant to
// the action, newest last.
func Decide(action string, resources []string, rules Rules) (Effect, Rules) {
	if len(resources) == 0 {
		resources = []string{DefaultResource}
	}
	var relevant Rules
	for _, rule := range rules {
		if Match(rule.Action, action) {
			relevant = append(relevant, rule)
		}
	}

	effect := Allow
	for _, resource := range resources {
		switch Evaluate(action, resource, rules).Effect {
		case Deny:
			return Deny, relevant
		case Allow:
		default:
			effect = Ask
		}
	}
	return effect, relevant
}

// Match reports whether a wildcard pattern matches value. It mirrors
// OpenCode's Wildcard.match: backslashes normalize to slashes, * matches any
// sequence (including /), ? matches one character, and a trailing " *" also
// matches the bare prefix ("git status *" matches "git status").
func Match(pattern, value string) bool {
	pattern = strings.ReplaceAll(pattern, "\\", "/")
	value = strings.ReplaceAll(value, "\\", "/")
	if pattern == value || pattern == "*" {
		return true
	}
	if pattern == "" {
		return value == ""
	}

	var b strings.Builder
	b.WriteString("(?s)^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	expr := b.String()
	// OpenCode special-cases a pattern ending in " *": it also matches the
	// command without arguments.
	if strings.HasSuffix(expr, " .*") {
		expr = strings.TrimSuffix(expr, " .*") + "( .*)?"
	}
	expr += "$"

	ok, err := regexp.MatchString(expr, value)
	return err == nil && ok
}

// ExpandHome expands a leading ~, $HOME, or ${HOME} in a pattern. Only the
// start is expanded; wildcards elsewhere are untouched.
func ExpandHome(s string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return s
	}
	switch {
	case s == "~", s == "$HOME", s == "${HOME}":
		return home
	case strings.HasPrefix(s, "~/"):
		return home + s[1:]
	case strings.HasPrefix(s, "$HOME/"):
		return home + s[len("$HOME"):]
	case strings.HasPrefix(s, "${HOME}/"):
		return home + s[len("${HOME}"):]
	}
	return s
}

// UnmarshalJSON accepts OpenCode's three permission config shapes:
//
//	"allow"                                         (one action, "*" resource)
//	{"bash": "ask", "edit": {"*": "deny", ...}}     (action -> effect/resources)
//	[{"action":"bash","resource":"*","effect":"ask"}] (native ruleset)
func (c *Config) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*c = nil
		return nil
	}

	switch trimmed[0] {
	case '"':
		var effect string
		if err := json.Unmarshal(data, &effect); err != nil {
			return err
		}
		rules := make(Config, 0, 1)
		if e, ok := parseEffect(effect); ok {
			rules = append(rules, Rule{Action: "*", Resource: DefaultResource, Effect: e})
		}
		*c = rules
		return nil
	case '[':
		var raw []Rule
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		rules := make(Config, 0, len(raw))
		for _, rule := range raw {
			rules = append(rules, normalizeRule(rule))
		}
		*c = rules
		return nil
	case '{':
		dec := json.NewDecoder(strings.NewReader(trimmed))
		rules, err := decodeRuleObject(dec)
		if err != nil {
			return err
		}
		*c = rules
		return nil
	default:
		return fmt.Errorf("permission: expected a string, object, or array, got %q", trimmed)
	}
}

func decodeRuleObject(dec *json.Decoder) (Config, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("permission: expected object")
	}

	var rules Config
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		action, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("permission: object keys must be strings")
		}
		action = CanonicalAction(action)

		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		value := strings.TrimSpace(string(raw))
		if value == "" {
			continue
		}
		switch value[0] {
		case '"':
			var effect string
			if err := json.Unmarshal(raw, &effect); err != nil {
				return nil, err
			}
			if e, ok := parseEffect(effect); ok {
				rules = append(rules, Rule{Action: action, Resource: DefaultResource, Effect: e})
			}
		case '{':
			nested, err := decodeResourceObject(action, value)
			if err != nil {
				return nil, err
			}
			rules = append(rules, nested...)
		case '[':
			// Tolerate {"bash": [{"resource": "...", "effect": "..."}]}.
			var entries []Rule
			if err := json.Unmarshal(raw, &entries); err != nil {
				return nil, err
			}
			for _, entry := range entries {
				entry.Action = action
				rules = append(rules, normalizeRule(entry))
			}
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return rules, nil
}

func decodeResourceObject(action, value string) (Config, error) {
	dec := json.NewDecoder(strings.NewReader(value))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("permission: expected resource object")
	}

	var rules Config
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		resource, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("permission: resource keys must be strings")
		}
		var effect string
		if err := dec.Decode(&effect); err != nil {
			return nil, err
		}
		if e, ok := parseEffect(effect); ok {
			rules = append(rules, Rule{
				Action:   action,
				Resource: ExpandHome(resource),
				Effect:   e,
			})
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return rules, nil
}

func normalizeRule(rule Rule) Rule {
	rule.Action = CanonicalAction(rule.Action)
	rule.Resource = ExpandHome(strings.TrimSpace(rule.Resource))
	if rule.Resource == "" {
		rule.Resource = DefaultResource
	}
	if e, ok := parseEffect(string(rule.Effect)); ok {
		rule.Effect = e
	} else {
		rule.Effect = Ask
	}
	return rule
}

func parseEffect(s string) (Effect, bool) {
	switch e := Effect(strings.ToLower(strings.TrimSpace(s))); e {
	case Allow, Ask, Deny:
		return e, true
	}
	return "", false
}

// MarshalJSON emits the compact OpenCode object form. Rules are grouped by
// action (first appearance order); order inside each action is preserved,
// which is all last-match-wins needs. A lone "*" resource encodes as a bare
// effect string.
func (c Config) MarshalJSON() ([]byte, error) {
	if len(c) == 0 {
		return []byte("{}"), nil
	}

	type group struct {
		action  string
		entries []Rule
	}
	var groups []group
	index := make(map[string]int)
	for _, raw := range c {
		rule := normalizeRule(raw)
		gi, ok := index[rule.Action]
		if !ok {
			gi = len(groups)
			index[rule.Action] = gi
			groups = append(groups, group{action: rule.Action})
		}
		// Replace a duplicate resource at its original position.
		replaced := false
		for i := range groups[gi].entries {
			if groups[gi].entries[i].Resource == rule.Resource {
				groups[gi].entries[i].Effect = rule.Effect
				replaced = true
				break
			}
		}
		if !replaced {
			groups[gi].entries = append(groups[gi].entries, rule)
		}
	}

	var b strings.Builder
	b.WriteByte('{')
	for gi, g := range groups {
		if gi > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(g.action)
		b.Write(key)
		b.WriteByte(':')
		if len(g.entries) == 1 && g.entries[0].Resource == DefaultResource {
			effect, _ := json.Marshal(g.entries[0].Effect)
			b.Write(effect)
			continue
		}
		b.WriteByte('{')
		for ei, entry := range g.entries {
			if ei > 0 {
				b.WriteByte(',')
			}
			rkey, _ := json.Marshal(entry.Resource)
			effect, _ := json.Marshal(entry.Effect)
			b.Write(rkey)
			b.WriteByte(':')
			b.Write(effect)
		}
		b.WriteByte('}')
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}
