//go:build unix

package schedule

import (
	"fmt"
	"os"
	"syscall"
)

// Lock is the single-owner scheduler lock. It is held for the lifetime of the
// process that runs jobs; management-only processes (the GUI) do not take it.
type Lock struct {
	f *os.File
}

// Acquire takes a non-blocking exclusive lock on path.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another anneal process owns the scheduler (%s): %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Close releases the lock.
func (l *Lock) Close() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
