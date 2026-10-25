// Package journey keeps a persistent timeline of self-improvement events
// (memory and skill writes, background reviews) under ~/.anneal/journey.jsonl.
package journey

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/uidbz/anneal/core/paths"
)

// Entry is one timeline row (kind + free-form fields).
type Entry struct {
	TS    string         `json:"ts"`
	Kind  string         `json:"kind"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Log appends JSONL and tails it back.
type Log struct {
	path string
	mu   sync.Mutex
}

func New() *Log {
	return &Log{path: filepath.Join(paths.Root(), "journey.jsonl")}
}

func (l *Log) Append(kind string, attrs map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := Entry{TS: time.Now().UTC().Format(time.RFC3339), Kind: kind, Attrs: attrs}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(data, '\n'))
}

// Tail returns the last n entries (oldest first).
func (l *Log) Tail(n int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 {
		n = 50
	}
	f, err := os.Open(l.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var all []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			all = append(all, e)
		}
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}
