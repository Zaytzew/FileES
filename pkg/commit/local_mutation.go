package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// Presence (including unreadable/corrupt state) fences all ordinary writers.
// The private coordinator alone may interpret or retire this evidence.
const RemoteTreePendingName = "remote-tree.pending.json"

var ErrRemoteTreePending = errors.New("local tree move recovery is pending")

func HasPendingRemoteTree(wc string) bool {
	_, err := os.Lstat(filepath.Join(wc, ".filees", "state", RemoteTreePendingName))
	return !errors.Is(err, os.ErrNotExist)
}

func HasPendingLocalMutation(wc string) bool {
	return HasPendingFileRelease(wc) || HasPendingRemoteTree(wc)
}

func LocalMutationPendingError(wc string) error {
	if HasPendingRemoteTree(wc) {
		return ErrRemoteTreePending
	}
	return ErrFileReleasePending
}

func IsLocalMutationPending(err error) bool {
	return errors.Is(err, ErrFileReleasePending) || errors.Is(err, ErrRemoteTreePending)
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
	if HasPendingFileRelease(wc) || HasPendingRemoteTree(wc) != recovery {
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
