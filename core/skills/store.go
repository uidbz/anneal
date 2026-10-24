// Package skills manages fyagent-authored skills: SKILL.md directories under
// ~/.fyagent/skills (agentskills-compatible, discovered by the dive skill
// loader), with create/patch/write_file/remove ops and optional write-approval
// staging under ~/.fyagent/pending/skills.
package skills

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
	"fyagent/core/paths"

	"github.com/deepnoodle-ai/dive/skill"
)

// Actions.
const (
	Create    = "create"
	Patch     = "patch"
	WriteFile = "write_file"
	Remove    = "remove"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,48}$`)

// Op is one requested skill change.
type Op struct {
	ID          string `json:"id"`
	TS          string `json:"ts"`
	Action      string `json:"action"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"` // create
	Content     string `json:"content,omitempty"`     // create: SKILL.md body; write_file: file content
	Path        string `json:"path,omitempty"`        // write_file: relative support path
	Old         string `json:"old,omitempty"`         // patch: unique substring
	New         string `json:"new,omitempty"`         // patch: replacement
	Reason      string `json:"reason,omitempty"`
	Source      string `json:"source"` // agent | review
}

// Info describes one installed skill.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Dir         string `json:"dir"`
	Chars       int    `json:"chars"`
}

// Store roots at paths.Skills(); mutex-serialized.
type Store struct {
	cfg config.Skills
	mu  sync.Mutex
}

func NewStore(cfg config.Skills) *Store { return &Store{cfg: cfg} }

func (s *Store) Root() string { return paths.Skills() }

// skillDir resolves a validated skill name to its directory.
func (s *Store) skillDir(name string) (string, error) {
	if !slugRe.MatchString(name) {
		return "", fmt.Errorf("skill name %q invalid (use 2-49 chars: lowercase letters, digits, dashes)", name)
	}
	return filepath.Join(s.Root(), name), nil
}

// List returns installed skills (SKILL.md parsed for description).
func (s *Store) List() []Info {
	ents, err := os.ReadDir(s.Root())
	if err != nil {
		return nil
	}
	var out []Info
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		f := filepath.Join(s.Root(), e.Name(), "SKILL.md")
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		info := Info{Name: e.Name(), Dir: filepath.Join(s.Root(), e.Name()), Chars: len(data)}
		if sk, err := skill.ParseContent(data, f); err == nil {
			info.Name = sk.Name
			info.Description = sk.Description
		}
		out = append(out, info)
	}
	return out
}

// View returns SKILL.md content for one skill.
func (s *Store) View(name string) (string, error) {
	d, err := s.skillDir(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(d, "SKILL.md"))
	if err != nil {
		return "", fmt.Errorf("skill %q not found", name)
	}
	return string(data), nil
}

// renderCreate assembles SKILL.md (frontmatter + body) and validates it
// parses as a skill.
func renderCreate(op *Op) (string, error) {
	body := strings.TrimSpace(op.Content)
	if body == "" {
		return "", fmt.Errorf("create needs content (the SKILL.md body)")
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n",
		op.Name, op.Description, body)
	if _, err := skill.ParseContent([]byte(content), filepath.Join(op.Name, "SKILL.md")); err != nil {
		return "", fmt.Errorf("resulting SKILL.md invalid: %v", err)
	}
	if len(content) > 40000 {
		return "", fmt.Errorf("SKILL.md too large (%d chars; keep under 40k)", len(content))
	}
	return content, nil
}

// Validate checks an op without touching disk.
func (s *Store) Validate(op *Op) error {
	d, err := s.skillDir(op.Name)
	if err != nil {
		return err
	}
	switch op.Action {
	case Create:
		if _, err := os.Stat(filepath.Join(d, "SKILL.md")); err == nil {
			return fmt.Errorf("skill %q already exists — use patch to improve it", op.Name)
		}
		if strings.TrimSpace(op.Description) == "" {
			return fmt.Errorf("create needs description (when should this skill be used?)")
		}
		if _, err := renderCreate(op); err != nil {
			return err
		}
	case Patch:
		if op.Old == "" || op.New == "" {
			return fmt.Errorf("patch needs old and new")
		}
		cur, err := s.View(op.Name)
		if err != nil {
			return err
		}
		n := strings.Count(cur, op.Old)
		if n == 0 {
			return fmt.Errorf("patch: substring not found in skill %q", op.Name)
		}
		if n > 1 {
			return fmt.Errorf("patch: substring appears %d times — give a unique one", n)
		}
		next := strings.Replace(cur, op.Old, op.New, 1)
		if _, err := skill.ParseContent([]byte(next), filepath.Join(d, "SKILL.md")); err != nil {
			return fmt.Errorf("resulting SKILL.md invalid: %v", err)
		}
	case WriteFile:
		if op.Path == "" || op.Content == "" {
			return fmt.Errorf("write_file needs path and content")
		}
		if err := safeRel(op.Path); err != nil {
			return err
		}
		if len(op.Content) > 100000 {
			return fmt.Errorf("support file too large (%d chars)", len(op.Content))
		}
	case Remove:
		if _, err := s.View(op.Name); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown action %q (create|patch|write_file|remove)", op.Action)
	}
	return nil
}

func safeRel(p string) error {
	if filepath.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
		return fmt.Errorf("path %q must stay inside the skill directory", p)
	}
	return nil
}

// Apply validates then performs the change; returns a human-readable result
// line (diff-ish preview).
func (s *Store) Apply(op *Op) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Validate(op); err != nil {
		return "", err
	}
	d, _ := s.skillDir(op.Name)
	switch op.Action {
	case Create:
		content, _ := renderCreate(op)
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(content), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("created skill %s (%d chars)", op.Name, len(content)), nil
	case Patch:
		cur, _ := s.View(op.Name)
		next := strings.Replace(cur, op.Old, op.New, 1)
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(next), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("patched skill %s (%d → %d chars)", op.Name, len(cur), len(next)), nil
	case WriteFile:
		f := filepath.Join(d, filepath.Clean(op.Path))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(f, []byte(op.Content), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("wrote %s in skill %s (%d chars)", op.Path, op.Name, len(op.Content)), nil
	case Remove:
		archive := filepath.Join(paths.SkillsArchive(), fmt.Sprintf("%s-%s", op.Name, time.Now().UTC().Format("20060102-150405")))
		if err := os.MkdirAll(paths.SkillsArchive(), 0o755); err != nil {
			return "", err
		}
		if err := os.Rename(d, archive); err != nil {
			return "", err
		}
		return fmt.Sprintf("archived skill %s → %s", op.Name, archive), nil
	}
	return "", fmt.Errorf("unreachable")
}

// Preview renders a short diff for approval UIs.
func (s *Store) Preview(op *Op) string {
	switch op.Action {
	case Create:
		body := op.Content
		if len(body) > 700 {
			body = body[:700] + " …"
		}
		return fmt.Sprintf("new skill %q — %s\n%s", op.Name, op.Description, body)
	case Patch:
		return fmt.Sprintf("skill %s: %q → %q", op.Name, clip(op.Old, 120), clip(op.New, 120))
	case WriteFile:
		return fmt.Sprintf("skill %s: write %s (%d chars)", op.Name, op.Path, len(op.Content))
	case Remove:
		return fmt.Sprintf("archive skill %s", op.Name)
	}
	return op.Action
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// --- staging ---

func (s *Store) pendDir() string { return filepath.Join(paths.Pending(), "skills") }

func (s *Store) Stage(op *Op) (*Op, error) {
	ents, _ := os.ReadDir(s.pendDir())
	count := 0
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") {
			count++
		}
	}
	if count >= 10 {
		return nil, fmt.Errorf("skill staging queue full (%d) — user must review first", count)
	}
	sum := make([]byte, 8)
	if _, err := rand.Read(sum); err != nil {
		return nil, err
	}
	op.ID = "skill-" + hex.EncodeToString(sum)
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

func (s *Store) Resolve(id string, approve bool) (*Op, string, error) {
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
	result := ""
	if approve {
		result, err = s.Apply(&op)
		if err != nil {
			return &op, "", err
		}
	}
	os.Remove(pf)
	return &op, result, nil
}
