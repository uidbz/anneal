// Package memory implements fyagent's bounded long-term memory: two small
// markdown stores (MEMORY.md for project/world facts, USER.md for user
// preferences), size-capped and injection-scanned, with optional write-approval
// staging under ~/.fyagent/pending/memory.
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"fyagent/core/config"
	"fyagent/core/fsutil"
	"fyagent/core/paths"
)

// Actions for Op.
const (
	Add    = "add"
	Patch  = "patch"
	Remove = "remove"
)

// Op is one requested memory change (applied directly or staged).
type Op struct {
	ID      string `json:"id"`
	TS      string `json:"ts"`
	Target  string `json:"target"`            // memory | user
	Action  string `json:"action"`            // add | patch | remove
	Content string `json:"content,omitempty"` // add: entry; patch: replacement
	Old     string `json:"old,omitempty"`     // patch/remove: unique substring
	New     string `json:"new,omitempty"`     // patch: replacement text
	Reason  string `json:"reason,omitempty"`  // why (shown in approval UI)
	Source  string `json:"source"`            // agent | review
}

// Store owns the two memory files (mutex-serialized, atomic writes). The
// config pointer is live: runtime settings changes apply without a restart.
type Store struct {
	cfg    *config.Memory
	dir    string
	mu     sync.Mutex
	pendMu sync.Mutex
}

func NewStore(cfg *config.Memory) *Store {
	if cfg == nil {
		cfg = &config.Memory{}
	}
	return &Store{cfg: cfg, dir: paths.Memories()}
}

// WriteApproval reports whether writes must be staged for user approval.
func (s *Store) WriteApproval() bool { return s.cfg.WriteApproval }

func (s *Store) file(target string) (string, error) {
	switch target {
	case "memory":
		return filepath.Join(s.dir, "MEMORY.md"), nil
	case "user":
		return filepath.Join(s.dir, "USER.md"), nil
	default:
		return "", fmt.Errorf("unknown memory target %q (use memory|user)", target)
	}
}

// Limit exposes the char cap for a target.
func (s *Store) Limit(target string) int { return s.limit(target) }

func (s *Store) limit(target string) int {
	if target == "user" {
		return s.cfg.UserCharLimit
	}
	return s.cfg.MemoryCharLimit
}

// Read returns the current content of one target ("" if absent).
func (s *Store) Read(target string) (string, error) {
	f, err := s.file(target)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(f)
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

// Snapshot renders the frozen reminder block content injected at session
// start (empty string when both stores are empty).
func (s *Store) Snapshot() string {
	m, _ := s.Read("memory")
	u, _ := s.Read("user")
	var sb strings.Builder
	if t := strings.TrimSpace(m); t != "" {
		sb.WriteString("<memory>\n" + t + "\n</memory>\n")
	}
	if t := strings.TrimSpace(u); t != "" {
		sb.WriteString("<user>\n" + t + "\n</user>\n")
	}
	return sb.String()
}

// injectionRe is the minimal prompt-injection scanner: memory is re-injected
// into every future session, so instruction-override and role-forgery
// patterns are rejected outright.
var injectionRe = regexp.MustCompile(`(?i)(ignore (all |any )?(previous|prior|earlier|above) instructions|disregard (the )?(rules|instructions)|you are now|new system prompt|system prompt|<system-reminder|^\s*(system|assistant|developer)\s*:|jailbreak|prompt injection)`)

func scanInjection(text string) error {
	if m := injectionRe.FindString(text); m != "" {
		return fmt.Errorf("content rejected: injection-suspect pattern %q (memory is re-injected into future sessions)", m)
	}
	return nil
}

// Validate checks one op against content rules (action fields, injection
// scanner, dup-guard, size cap) without touching disk.
func (s *Store) Validate(op *Op) error {
	if _, err := s.file(op.Target); err != nil {
		return err
	}
	if err := scanInjection(op.Content + "\n" + op.New); err != nil {
		return err
	}
	cur, err := s.Read(op.Target)
	if err != nil {
		return err
	}
	switch op.Action {
	case Add:
		e := strings.TrimSpace(op.Content)
		if e == "" {
			return fmt.Errorf("add needs content")
		}
		if entryExists(cur, e) {
			return fmt.Errorf("entry already in %s memory (dup-guard)", op.Target)
		}
		if len(cur)+len(e)+3 > s.limit(op.Target) {
			return fmt.Errorf("add would exceed %s cap: %d + %d > %d chars — remove or shorten first",
				op.Target, len(cur), len(e), s.limit(op.Target))
		}
	case Patch:
		if op.Old == "" || op.New == "" {
			return fmt.Errorf("patch needs old and new")
		}
		n := strings.Count(cur, op.Old)
		if n == 0 {
			return fmt.Errorf("patch: substring not found in %s memory", op.Target)
		}
		if n > 1 {
			return fmt.Errorf("patch: substring appears %d times — give a unique one", n)
		}
		next := strings.Replace(cur, op.Old, op.New, 1)
		if len(next) > s.limit(op.Target) {
			return fmt.Errorf("patch would exceed %s cap: %d > %d chars", op.Target, len(next), s.limit(op.Target))
		}
	case Remove:
		if op.Old == "" {
			return fmt.Errorf("remove needs old")
		}
		n := strings.Count(cur, op.Old)
		if n == 0 {
			return fmt.Errorf("remove: substring not found in %s memory", op.Target)
		}
		if n > 1 {
			return fmt.Errorf("remove: substring appears %d times — give a unique one", n)
		}
	default:
		return fmt.Errorf("unknown action %q (use add|patch|remove)", op.Action)
	}
	return nil
}

func entryExists(cur, entry string) bool {
	norm := strings.ToLower(strings.Join(strings.Fields(entry), " "))
	for _, line := range strings.Split(cur, "\n") {
		l := strings.ToLower(strings.Join(strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "- ")), " "))
		if l == norm {
			return true
		}
	}
	return false
}

// Apply validates then writes; returns the new file content.
func (s *Store) Apply(op *Op) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Validate(op); err != nil {
		return "", err
	}
	f, _ := s.file(op.Target)
	cur, err := s.Read(op.Target)
	if err != nil {
		return "", err
	}
	var next string
	switch op.Action {
	case Add:
		e := strings.TrimSpace(op.Content)
		if cur != "" && !strings.HasSuffix(cur, "\n") {
			cur += "\n"
		}
		next = cur + "- " + e + "\n"
	case Patch:
		next = strings.Replace(cur, op.Old, op.New, 1)
	case Remove:
		next = strings.Replace(cur, op.Old, "", 1)
	}
	if err := writeFileAtomic(f, next); err != nil {
		return "", err
	}
	return next, nil
}

func writeFileAtomic(f, content string) error {
	return fsutil.WriteFileAtomic(f, []byte(content), 0o600)
}

// --- staging (write_approval) ---

func (s *Store) pendDir() string { return filepath.Join(paths.Pending(), "memory") }

// opIDRe is the only id shape Stage generates. Resolve is reachable from the
// UI/script API, so never build a path from an unvalidated id.
var opIDRe = regexp.MustCompile(`^mem-[0-9a-f]{16}$`)

// Stage persists an op awaiting user approval; returns the stored op.
func (s *Store) Stage(op *Op) (*Op, error) {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	list, err := s.ListPending()
	if err != nil {
		return nil, err
	}
	if len(list) >= 10 {
		return nil, fmt.Errorf("memory staging queue full (%d) — user must review first", len(list))
	}
	for _, p := range list {
		if p.Target == op.Target && p.Action == op.Action &&
			p.Content == op.Content && p.Old == op.Old && p.New == op.New {
			return p, nil // dedupe identical pending requests
		}
	}
	sum := make([]byte, 8)
	if _, err := rand.Read(sum); err != nil {
		return nil, err
	}
	op.ID = "mem-" + hex.EncodeToString(sum)
	op.TS = time.Now().UTC().Format(time.RFC3339)
	if op.Source == "" {
		op.Source = "agent"
	}
	if err := os.MkdirAll(s.pendDir(), 0o755); err != nil {
		return nil, err
	}
	data, _ := json.MarshalIndent(op, "", "  ")
	if err := os.WriteFile(filepath.Join(s.pendDir(), op.ID+".json"), data, 0o600); err != nil {
		return nil, err
	}
	return op, nil
}

// ListPending returns staged ops (oldest first).
func (s *Store) ListPending() ([]*Op, error) {
	ents, err := os.ReadDir(s.pendDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Op
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.pendDir(), e.Name()))
		if err != nil {
			continue
		}
		var op Op
		if err := json.Unmarshal(data, &op); err != nil {
			continue
		}
		out = append(out, &op)
	}
	return out, nil
}

// resolve applies (approve) or drops (reject) one staged op, removing its file.
func (s *Store) Resolve(id string, approve bool) (*Op, string, error) {
	if !opIDRe.MatchString(id) {
		return nil, "", fmt.Errorf("bad pending op id %q", id)
	}
	pf := filepath.Join(s.pendDir(), id+".json")
	data, err := os.ReadFile(pf)
	if err != nil {
		return nil, "", fmt.Errorf("pending op %s not found", id)
	}
	var op Op
	if err := json.Unmarshal(data, &op); err != nil {
		os.Remove(pf)
		return nil, "", fmt.Errorf("corrupt pending op: %v", err)
	}
	content := ""
	if approve {
		content, err = s.Apply(&op)
		if err != nil {
			// The UI drops the card on memory.resolved, so keeping the file
			// would orphan it. Drop it; the agent can request a new write.
			os.Remove(pf)
			return &op, "", err
		}
	}
	os.Remove(pf)
	return &op, content, nil
}
