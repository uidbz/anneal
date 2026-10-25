// Package harnesslib is the improvement library (Harness² harnessing-level
// recursion): one entry per evidence-supported improvement step, holding
// the adopted harness components plus EXPERIENCE.md (situation, lessons,
// evidence). Entries are RE-DRAFTED FROM by future Propose/Compose steps —
// never inherited wholesale. The global ~/.fyagent/harness only changes
// through an explicit promote.
package harnesslib

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fyagent/core/paths"
)

type Store struct {
	root string
}

func New(root string) *Store { return &Store{root: root} }

func (s *Store) Root() string { return s.root }

// Meta describes one entry.
type Meta struct {
	ID          string   `json:"id"`
	Task        string   `json:"task"`
	SessionID   string   `json:"session_id,omitempty"`
	RunID       string   `json:"run_id,omitempty"`
	HarnessHash string   `json:"harness_hash,omitempty"`
	Components  []string `json:"components"`
	CreatedAt   string   `json:"created_at"`
}

type Info struct {
	ID         string `json:"id"`
	Situation  string `json:"situation"`
	Components string `json:"components"`
	Chars      int    `json:"chars"`
}

func (s *Store) Seed() error {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return err
	}
	idx := filepath.Join(s.root, "INDEX.md")
	if _, err := os.Stat(idx); err != nil {
		return os.WriteFile(idx, []byte("# fyagent harness improvement library\n\nOne entry per evidence-supported step; see README in any entry dir.\n"), 0o644)
	}
	return nil
}

func slug(s string, n int) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		} else if sb.Len() > 0 && sb.String()[sb.Len()-1] != '-' {
			sb.WriteByte('-')
		}
		if sb.Len() >= n {
			break
		}
	}
	return strings.Trim(sb.String(), "-")
}

// CreateEntry records an adopted successor + its experience note.
// caller supplies only plain data (no import cycle with core/improve).
func (s *Store) CreateEntry(successorDir, task, sessionID, runID, harnessHash, experience string) (string, error) {
	if strings.TrimSpace(experience) == "" {
		return "", fmt.Errorf("harnesslib: empty experience")
	}
	files := mdFiles(successorDir)
	if len(files) == 0 {
		return "", fmt.Errorf("harnesslib: successor dir has no components")
	}
	id := time.Now().UTC().Format("20060102-150405") + "-" + slug(task, 28)
	dir := filepath.Join(s.root, id)
	if err := os.MkdirAll(filepath.Join(dir, "harness"), 0o755); err != nil {
		return "", err
	}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(successorDir, f))
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "harness", f), data, 0o644); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "EXPERIENCE.md"), []byte(experience), 0o644); err != nil {
		return "", err
	}
	m := Meta{ID: id, Task: clip(task, 300), SessionID: sessionID, RunID: runID,
		HarnessHash: harnessHash, Components: files,
		CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644); err != nil {
		return "", err
	}
	if err := s.rebuildIndex(); err != nil {
		return id, err
	}
	return id, nil
}

func (s *Store) rebuildIndex() error {
	var sb strings.Builder
	sb.WriteString("# fyagent harness improvement library\n\nnewest first; read an entry's EXPERIENCE.md before reusing its components\n\n")
	for _, info := range s.List() {
		fmt.Fprintf(&sb, "- %s — %s (components: %s)\n", info.ID, info.Situation, info.Components)
	}
	return os.WriteFile(filepath.Join(s.root, "INDEX.md"), []byte(sb.String()), 0o644)
}

func (s *Store) List() []Info {
	entries, _ := os.ReadDir(s.root)
	var out []Info
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "promote-backup" {
			continue
		}
		metaB, err := os.ReadFile(filepath.Join(s.root, e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var m Meta
		if json.Unmarshal(metaB, &m) != nil {
			continue
		}
		exp, _ := os.ReadFile(filepath.Join(s.root, e.Name(), "EXPERIENCE.md"))
		situation := firstSituation(string(exp))
		if situation == "" {
			situation = clip(m.Task, 70)
		}
		out = append(out, Info{ID: e.Name(), Situation: situation,
			Components: strings.Join(m.Components, " "), Chars: len(exp)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

func firstSituation(exp string) string {
	in := false
	for _, l := range strings.Split(exp, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(strings.ToLower(l), "## situation") {
			in = true
			continue
		}
		if in {
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			return clip(l, 90)
		}
	}
	return ""
}

// Index returns the library index text (bounded) for editor prompts.
func (s *Store) Index(maxChars int) string {
	b, _ := os.ReadFile(filepath.Join(s.root, "INDEX.md"))
	return clip(strings.TrimSpace(string(b)), maxChars)
}

// Context ranks entries against the task text and returns up to max
// EXPERIENCE excerpts (local token overlap; tie indexing is write-side
// for session_search visibility). "" when the library is empty.
func (s *Store) Context(task string, max int, maxChars int) string {
	infos := s.List()
	if len(infos) == 0 {
		return ""
	}
	toks := map[string]bool{}
	for _, t := range tokens(task) {
		toks[t] = true
	}
	type scored struct {
		Info
		score int
	}
	var ranked []scored
	for _, inf := range infos {
		sc := 0
		exp, _ := os.ReadFile(filepath.Join(s.root, inf.ID, "EXPERIENCE.md"))
		for _, t := range tokens(string(exp)) {
			if toks[t] {
				sc++
			}
		}
		sc += len(inf.Components) // mild recency/completeness nudge
		ranked = append(ranked, scored{inf, sc})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	var sb strings.Builder
	total := 0
	n := 0
	for _, r := range ranked {
		if n >= max {
			break
		}
		exp, _ := os.ReadFile(filepath.Join(s.root, r.ID, "EXPERIENCE.md"))
		block := fmt.Sprintf("### entry %s\n%s\n", r.ID, strings.TrimSpace(string(exp)))
		if total+len(block) > maxChars {
			block = clip(block, maxChars-total) + "\n"
		}
		sb.WriteString(block)
		total += len(block)
		n++
		if total >= maxChars {
			break
		}
	}
	return strings.TrimSpace(sb.String())
}

// EntryDir returns the adopted component dir of one entry.
func (s *Store) EntryDir(id string) (string, error) {
	d := filepath.Join(s.root, id, "harness")
	if id == "" || strings.ContainsAny(id, "/\\") {
		return "", fmt.Errorf("harnesslib: bad entry id %q", id)
	}
	if _, err := os.Stat(d); err != nil {
		return "", fmt.Errorf("harnesslib: no entry %q", id)
	}
	return d, nil
}

// Promote copies an entry's components into the GLOBAL harness directory
// (the only path by which agent-authored changes become permanent).
// Replaced files are backed up under promote-backup/<ts>/.
func (s *Store) Promote(id string) (backupDir string, err error) {
	src, err := s.EntryDir(id)
	if err != nil {
		return "", err
	}
	dst := paths.Harness()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}
	ts := time.Now().UTC().Format("20060102-150405")
	backupDir = filepath.Join(s.root, "promote-backup", ts)
	for _, f := range mdFiles(src) {
		target := filepath.Join(dst, f)
		if old, err := os.ReadFile(target); err == nil {
			if err := os.MkdirAll(backupDir, 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(backupDir, f), old, 0o644); err != nil {
				return "", err
			}
		}
		data, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return "", err
		}
	}
	return backupDir, nil
}

func mdFiles(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && e.Name() != "decisions.md" && e.Name() != "README.md" {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func clip(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func tokens(s string) []string {
	var out []string
	cur := strings.Builder{}
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
			continue
		}
		if cur.Len() >= 3 {
			out = append(out, cur.String())
		}
		cur.Reset()
	}
	if cur.Len() >= 3 {
		out = append(out, cur.String())
	}
	return out
}
