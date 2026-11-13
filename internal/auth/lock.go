package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// A file lock serializes rotating refresh grants across processes and Service
// instances. Closing the descriptor releases the lock, including on crashes.
func (s *Service) lockProfile(ctx context.Context, id string) (*os.File, error) {
	if !profilePattern.MatchString(id) {
		return nil, authError("invalid credential profile")
	}
	if err := s.ensureDirectory(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.directory(), id+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // G304: validated profile ID confines this owner-only lock to the credential directory.
	if err != nil {
		return nil, fmt.Errorf("open credential lock: %w", err)
	}
	if err = waitLock(ctx, file); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func waitLock(ctx context.Context, file *os.File) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("lock credentials: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
