package commit

import (
	"context"
	"errors"

	"filees/pkg/client"
)

var ErrLocalStatusBusy = errors.New("working copy writer is busy")

// WithWorkingCopyWrite admits daemon-side property migration and read-only
// pipeline updates to the same guard. The caller must not call another
// locking Service method from operation. This is not a new remote command.
func (s *Service) WithWorkingCopyWrite(ctx context.Context, wc string, operation func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.wcOpMu.Lock()
	defer s.wcOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.wc != wc || s.commitWriterBlocked || operation == nil {
		return ErrLocalStatusBusy
	}
	if HasPendingLocalMutation(wc) {
		return LocalMutationPendingError(wc)
	}
	return operation()
}

// WithLocalStatus is an internal metadata observer, not an IPC operation.
// Keep status and its confirmation under the publication/update/adopt lock:
// file USN alone cannot detect an intervening SVN property edit. The callback
// must finish its helper ACK, or stop and reap that helper, before returning.
// A busy/recovering WC is skipped; observers never queue or confirm absence.
func (s *Service) WithLocalStatus(ctx context.Context, wc string, paths []string, confirm func([]client.StatusEntry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(paths) == 0 || len(paths) > 64 || confirm == nil {
		return errors.New("invalid local status batch")
	}
	if !s.wcOpMu.TryLock() {
		return ErrLocalStatusBusy
	}
	defer s.wcOpMu.Unlock()
	if s.wc != wc || s.commitWriterBlocked || s.Cli == nil {
		return ErrLocalStatusBusy
	}
	if HasPendingLocalMutation(wc) {
		return LocalMutationPendingError(wc)
	}
	rows, err := s.Cli.Status(ctx, wc, paths)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return confirm(rows)
}
