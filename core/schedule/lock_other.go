//go:build !unix

package schedule

// Lock is a no-op on platforms without advisory file locking.
type Lock struct{}

// Acquire returns a no-op lock.
func Acquire(string) (*Lock, error) { return &Lock{}, nil }

// Close is a no-op.
func (l *Lock) Close() {}
