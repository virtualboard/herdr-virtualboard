package runs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// Recover quarantines malformed or incomplete supported-format documents only
// on an explicit recovery request.
// Future schemas are never discarded: a newer compatible binary must read them.
func (s *Store) Recover(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	backup := ""
	err := withOSLock(ctx, s.path+".mutex", func() error {
		raw, err := os.ReadFile(s.path)
		if err != nil {
			return err
		}
		_, decodeErr := decodeDocument(raw)
		if decodeErr == nil {
			return fmt.Errorf("run store is readable; recovery is unnecessary")
		}
		if errors.Is(decodeErr, ErrUnsupportedFormat) {
			return fmt.Errorf("recovery will not replace this store: %w", decodeErr)
		}

		backup = fmt.Sprintf("%s.corrupt-%d", s.path, time.Now().UnixNano())
		if err := os.Rename(s.path, backup); err != nil {
			return err
		}
		if err := s.save(&document{Version: version}); err != nil {
			if restoreErr := os.Rename(backup, s.path); restoreErr != nil {
				return fmt.Errorf("initialize recovered store: %v; restore: %v; original retained at %s", err, restoreErr, backup)
			}
			return err
		}
		return nil
	})
	return backup, err
}
