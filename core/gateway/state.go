package gateway

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/uidbz/anneal/core/fsutil"
)

// pendingApproval remembers the last approval card shown in a chat, so a
// numeric reply (1/2/3/0) can answer it.
type pendingApproval struct {
	Server string    `json:"server"`
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
}

// persisted is the small, human-inspectable gateway state file.
type persisted struct {
	Active  map[string]string          `json:"active_server,omitempty"`
	LastIn  map[string]string          `json:"last_in_id,omitempty"`
	Pending map[string]pendingApproval `json:"pending,omitempty"`
	LastRun map[string]string          `json:"last_run_id,omitempty"` // server -> delivered run-record id
}

// stateStore serializes state changes and writes atomically.
type stateStore struct {
	path string
	mu   sync.Mutex
	data persisted
}

func openState(path string) (*stateStore, error) {
	s := &stateStore{path: path}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &s.data)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if s.data.Active == nil {
		s.data.Active = map[string]string{}
	}
	if s.data.LastIn == nil {
		s.data.LastIn = map[string]string{}
	}
	if s.data.Pending == nil {
		s.data.Pending = map[string]pendingApproval{}
	}
	if s.data.LastRun == nil {
		s.data.LastRun = map[string]string{}
	}
	return s, nil
}

func (s *stateStore) saveLocked() {
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return
	}
	_ = fsutil.WriteFileAtomic(s.path, append(data, '\n'), 0o600)
}

// activeServer returns the chat's selected server, or fallback.
func (s *stateStore) activeServer(chat, fallback string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name := s.data.Active[chat]; name != "" {
		return name
	}
	return fallback
}

func (s *stateStore) setActiveServer(chat, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Active[chat] = name
	s.saveLocked()
}

// Cursor loads/saves an opaque delivery cursor (ntfy's last message id).
func (s *stateStore) Cursor(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.LastIn[key]
}

func (s *stateStore) SetCursor(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value == "" {
		if _, ok := s.data.LastIn[key]; ok {
			delete(s.data.LastIn, key)
			s.saveLocked()
		}
		return
	}
	if s.data.LastIn[key] == value {
		return
	}
	s.data.LastIn[key] = value
	s.saveLocked()
}

func (s *stateStore) setPending(chat string, p pendingApproval) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Pending[chat] = p
	s.saveLocked()
}

func (s *stateStore) pending(chat string) (pendingApproval, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.data.Pending[chat]
	return p, ok
}

func (s *stateStore) clearPending(chat, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.data.Pending[chat]; ok && (id == "" || p.ID == id) {
		delete(s.data.Pending, chat)
		s.saveLocked()
	}
}

func (s *stateStore) lastRun(server string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.LastRun[server]
}

func (s *stateStore) setLastRun(server, id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.LastRun[server] == id {
		return
	}
	s.data.LastRun[server] = id
	s.saveLocked()
}
