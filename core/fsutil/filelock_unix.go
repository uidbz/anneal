//go:build unix

package fsutil

import (
	"os"
	"syscall"
)

// WithFileLock runs fn while holding an exclusive advisory lock on path.lock.
// Several anneal processes may share one state directory (two local windows);
// the lock keeps read-modify-write files such as permissions.json and
// config.json from interleaving. A lock failure runs fn unlocked: saving must
// not fail just because the lock file cannot be created.
func WithFileLock(path string, fn func() error) error {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fn()
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fn()
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}
