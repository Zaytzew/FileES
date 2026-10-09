package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"filees/pkg/watcher"
)

// Presence (including unreadable/corrupt state) fences all ordinary writers.
// The private coordinator alone may interpret or retire this evidence.
const RemoteTreePendingName = "remote-tree.pending.json"
const RemoteFileDeletePendingName = "remote-file-delete.pending.json"

var ErrRemoteTreePending = errors.New("local tree move recovery is pending")
var ErrRemoteFileDeletePending = errors.New("local file deletion recovery is pending")

func HasPendingRemoteFileDelete(wc string) bool {
	_, err := os.Lstat(filepath.Join(wc, ".filees", "state", RemoteFileDeletePendingName))
	return !errors.Is(err, os.ErrNotExist)
}

func HasPendingRemoteTree(wc string) bool {
	_, err := os.Lstat(filepath.Join(wc, ".filees", "state", RemoteTreePendingName))
	return !errors.Is(err, os.ErrNotExist)
}

func HasPendingLocalMutation(wc string) bool {
	return HasPendingFileRelease(wc) || HasPendingRemoteTree(wc) || HasPendingRemoteFileDelete(wc)
}

func LocalMutationPendingError(wc string) error {
	if HasPendingRemoteFileDelete(wc) {
		return ErrRemoteFileDeletePending
	}
	if HasPendingRemoteTree(wc) {
		return ErrRemoteTreePending
	}
	return ErrFileReleasePending
}

func IsLocalMutationPending(err error) bool {
	return errors.Is(err, ErrFileReleasePending) || errors.Is(err, ErrRemoteTreePending) || errors.Is(err, ErrRemoteFileDeletePending)
}

// Internal only. The callback must hold the scanner/local-object guards, prove
// the binding and persist its intent before invoking SVN. This method gives no
// permission to mutate paths, clear evidence or replay an uncertain attempt.
// Unlike release recovery, tree recovery must not enter through WithFileRelease.
func (s *Service) WithRemoteTreeMutation(ctx context.Context, wc string, recovery bool, operation func() error) error {
	if operation == nil {
		return errors.New("missing remote tree operation")
	}
	leave, err := s.Pause.Enter(ctx, s.repoID, false)
	if err != nil {
		return err
	}
	defer leave()
	release, err := s.Admission.EnterContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	s.wcOpMu.Lock()
	defer s.wcOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.wc != wc || s.Cli == nil || s.commitWriterBlocked || HasUnresolvedCommit(wc) {
		return ErrLocalStatusBusy
	}
	if HasPendingRemoteFileDelete(wc) || HasPendingFileRelease(wc) || HasPendingRemoteTree(wc) != recovery {
		return LocalMutationPendingError(wc)
	}
	if !recovery {
		s.mu.Lock()
		staged := len(s.staging) != 0
		s.mu.Unlock()
		if staged {
			return ErrLocalStatusBusy
		}
	}
	return operation()
}

// Private deletion journal entry. Preparation and completion use the same real
// writer, but completion additionally requires its own durable pending fence.
// This supplies no scanner/OS proof, retry permission or evidence retirement.
func (s *Service) WithRemoteFileDeleteMutation(ctx context.Context, wc string, recovery bool, operation func() error) error {
	if operation == nil {
		return errors.New("missing remote file deletion operation")
	}
	leave, err := s.Pause.Enter(ctx, s.repoID, false)
	if err != nil {
		return err
	}
	defer leave()
	release, err := s.Admission.EnterContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	s.wcOpMu.Lock()
	defer s.wcOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.wc != wc || s.Cli == nil || s.commitWriterBlocked || HasUnresolvedCommit(wc) {
		return ErrLocalStatusBusy
	}
	if HasPendingFileRelease(wc) || HasPendingRemoteTree(wc) {
		return LocalMutationPendingError(wc)
	}
	if HasPendingRemoteFileDelete(wc) != recovery {
		return ErrRemoteFileDeletePending
	}
	if !recovery {
		s.mu.Lock()
		staged := len(s.staging) != 0
		s.mu.Unlock()
		if staged {
			return ErrLocalStatusBusy
		}
	}
	return operation()
}

// Internal tree coordinator entry, retaining the real writer AND scanner
// locks through the callback and durable baseline ACK. No fallback without a
// publication scanner. This does not retire the owner's pending marker.
func (s *Service) WithRemoteTreeMove(ctx context.Context, wc, source, destination string, recovery *watcher.TreeMoveSnapshot, operation func(*watcher.TreeMoveSnapshot) error, completion ...func() error) error {
	if operation == nil || len(completion) > 1 {
		return errors.New("missing tree move coordinator")
	}
	return s.WithRemoteTreeMutation(ctx, wc, recovery != nil, func() error {
		var err error
		if recovery != nil {
			if recovery.WC != wc || recovery.Source != source || recovery.Destination != destination {
				return errors.New("tree recovery selection differs from its bound snapshot")
			}
			if s.RecoverTreeMove == nil {
				return errors.ErrUnsupported
			}
			err = s.RecoverTreeMove(ctx, recovery, func() error { return operation(recovery) })
		} else {
			if s.FenceTreeMove == nil {
				return errors.ErrUnsupported
			}
			err = s.FenceTreeMove(ctx, source, destination, operation)
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Scanner ACK is now durable; keep the real writer while the private
		// coordinator verifies completion and retires only its exact fence.
		if len(completion) == 1 && completion[0] != nil {
			return completion[0]()
		}
		return nil
	})
}
