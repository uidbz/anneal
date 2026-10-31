//go:build !unix

package fsutil

// WithFileLock runs fn (no advisory locking on this platform).
func WithFileLock(_ string, fn func() error) error { return fn() }
