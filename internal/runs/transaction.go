package runs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// WithRunTransaction serializes orchestration, including external side effects,
// without holding the store's read-modify-write lock across subprocess calls.
// Closing a crashed process releases this OS lock; never unlink its inode.
func (s *Store) WithRunTransaction(ctx context.Context, id string, fn func() error) error {
	dir := s.path + ".transactions"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("%x.mutex", sha256.Sum256([]byte(id))))
	return withOSLock(ctx, path, fn)
}

func withOSLock(ctx context.Context, path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("wait for run transaction: %w", waitCtx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	return fn()
}
