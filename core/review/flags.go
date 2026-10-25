package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Flag is one reviewer-observed harness deficiency: a work-style gap
// (verification, repetition, planning) worth feeding to the /improve
// loop. The reviewer never edits the harness itself — flags are its only
// output on this path (paper §2.2: the improvement loop decides, on
// probe evidence, not the reviewer).
type Flag struct {
	ID         string    `json:"id"`
	Session    string    `json:"session"`
	Gap        string    `json:"gap"`
	At         time.Time `json:"at"`
	ConsumedBy string    `json:"consumed_by,omitempty"` // improve run id
}

// FlagStore persists flags to a single JSON file (oldest unconsumed first).
type FlagStore struct {
	path   string
	mu     sync.Mutex
	flags  []Flag
	loaded bool
}

func NewFlagStore(path string) *FlagStore { return &FlagStore{path: path} }

func (s *FlagStore) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &s.flags)
}

func (s *FlagStore) save() error {
	tmp := s.path + ".tmp"
	b, err := json.MarshalIndent(s.flags, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Add queues a flag (capped: oldest consumed entries are pruned).
func (s *FlagStore) Add(session, gap string) Flag {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	f := Flag{ID: fmt.Sprintf("f%d", time.Now().UnixNano()), Session: session,
		Gap: gap, At: time.Now()}
	s.flags = append(s.flags, f)
	if len(s.flags) > 60 {
		keep := []Flag{}
		consumed := 0
		for _, x := range s.flags {
			if x.ConsumedBy != "" && consumed < 10 {
				consumed++
				keep = append(keep, x)
				continue
			}
			if x.ConsumedBy == "" {
				keep = append(keep, x)
			}
		}
		if len(keep) > 50 {
			keep = keep[len(keep)-50:]
		}
		s.flags = keep
	}
	_ = s.save()
	return f
}

// Pending lists unconsumed flags, oldest first.
func (s *FlagStore) Pending(n int) []Flag {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	var out []Flag
	for _, f := range s.flags {
		if f.ConsumedBy == "" {
			out = append(out, f)
			if n > 0 && len(out) == n {
				break
			}
		}
	}
	return out
}

// Consume marks up to n oldest pending flags as used by improve run runid
// and returns them (the improve loop's editors see them as hints; the
// probe evidence still decides what actually changes).
func (s *FlagStore) Consume(runid string, n int) []Flag {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	var taken []Flag
	for i := range s.flags {
		if s.flags[i].ConsumedBy == "" {
			s.flags[i].ConsumedBy = runid
			taken = append(taken, s.flags[i])
			if n > 0 && len(taken) == n {
				break
			}
		}
	}
	if len(taken) > 0 {
		_ = s.save()
	}
	return taken
}
