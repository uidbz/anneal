package schedule

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/uidbz/anneal/core/fsutil"
)

const jobsVersion = 1

type jobsFile struct {
	Version int   `json:"version"`
	Jobs    []Job `json:"jobs"`
}

// Store persists jobs and the append-only run ledger under one directory.
type Store struct {
	Dir      string
	JobsPath string
	RunsPath string
	LockPath string
}

// NewStore creates a store rooted at dir (typically paths.Schedules()).
func NewStore(dir string) *Store {
	return &Store{
		Dir:      dir,
		JobsPath: filepath.Join(dir, "jobs.json"),
		RunsPath: filepath.Join(dir, "runs.jsonl"),
		LockPath: filepath.Join(dir, "scheduler.lock"),
	}
}

// Load reads all jobs. A missing file is an empty store.
func (s *Store) Load() ([]Job, error) {
	var out []Job
	err := fsutil.WithFileLock(s.JobsPath, func() error {
		jobs, err := s.loadUnlocked()
		out = jobs
		return err
	})
	return out, err
}

func (s *Store) loadUnlocked() ([]Job, error) {
	data, err := os.ReadFile(s.JobsPath)
	if os.IsNotExist(err) {
		return []Job{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc jobsFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode %s: %w", s.JobsPath, err)
	}
	jobs := doc.Jobs
	if jobs == nil {
		jobs = []Job{}
	}
	sort.SliceStable(jobs, func(i, k int) bool {
		a, b := jobs[i], jobs[k]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	return jobs, nil
}

// Save replaces the job document atomically.
func (s *Store) Save(jobs []Job) error {
	return fsutil.WithFileLock(s.JobsPath, func() error {
		data, err := json.MarshalIndent(jobsFile{Version: jobsVersion, Jobs: jobs}, "", "  ")
		if err != nil {
			return err
		}
		return fsutil.WriteFileAtomic(s.JobsPath, append(data, '\n'), 0o600)
	})
}

// AppendRun appends one record to runs.jsonl (JSONL, one record per line).
func (s *Store) AppendRun(rec RunRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return fsutil.WithFileLock(s.RunsPath, func() error {
		f, err := os.OpenFile(s.RunsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.Write(append(data, '\n')); err != nil {
			return err
		}
		return f.Sync()
	})
}

// Runs returns up to limit records, newest first. limit <= 0 means all.
func (s *Store) Runs(limit int) ([]RunRecord, error) {
	f, err := os.Open(s.RunsPath)
	if os.IsNotExist(err) {
		return []RunRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []RunRecord
	sc := bufio.NewScanner(f)
	// Run records carry a capped text excerpt; allow long lines.
	sc.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec RunRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // a torn final line must not hide earlier results
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// FilterRunsSince returns records started at or after t, newest first.
func (s *Store) FilterRunsSince(t int64, limit int) ([]RunRecord, error) {
	all, err := s.Runs(0)
	if err != nil {
		return nil, err
	}
	out := make([]RunRecord, 0, len(all))
	for _, r := range all {
		if r.StartedAt.Unix() >= t {
			out = append(out, r)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}
