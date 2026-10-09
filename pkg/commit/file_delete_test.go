package commit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileDeletePendingFencesOrdinaryWritersAndOtherRecovery(t *testing.T) {
	wc := t.TempDir()
	s := &Service{Cli: &statusClient{}}
	s.BindWorkingCopy("repo", wc)
	if err := s.WithRemoteFileDeleteMutation(t.Context(), wc, true, func() error { t.Fatal("recovery without intent"); return nil }); !errors.Is(err, ErrRemoteFileDeletePending) {
		t.Fatal(err)
	}
	s.staging = map[string]*stageItem{"unrelated": {}}
	if err := s.WithRemoteFileDeleteMutation(t.Context(), wc, false, func() error { t.Fatal("staged work admitted"); return nil }); !errors.Is(err, ErrLocalStatusBusy) {
		t.Fatal(err)
	}
	s.staging = nil
	if err := s.WithRemoteFileDeleteMutation(t.Context(), wc, false, func() error {
		if s.wcOpMu.TryLock() {
			t.Fatal("delete prepare escaped real writer")
		}
		if err := os.MkdirAll(filepath.Join(wc, ".filees", "state"), 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(wc, ".filees", "state", RemoteFileDeletePendingName), []byte("corrupt evidence also fences"), 0600)
	}); err != nil {
		t.Fatal(err)
	}
	if !HasPendingLocalMutation(wc) || HasPendingFileRelease(wc) || HasPendingRemoteTree(wc) {
		t.Fatal("fences conflated")
	}
	call := func() error { t.Fatal("ordinary writer or unrelated recovery ran"); return nil }
	for _, recovery := range []bool{false, true} {
		if err := s.WithFileRelease(t.Context(), wc, "file", recovery, call); !errors.Is(err, ErrRemoteFileDeletePending) {
			t.Fatal(err)
		}
		if err := s.WithRemoteTreeMutation(t.Context(), wc, recovery, call); !errors.Is(err, ErrRemoteFileDeletePending) {
			t.Fatal(err)
		}
	}
	if err := s.WithWorkingCopyWrite(t.Context(), wc, call); !errors.Is(err, ErrRemoteFileDeletePending) {
		t.Fatal(err)
	}
	if err := s.WithRemoteFileDeleteMutation(t.Context(), wc, false, call); !errors.Is(err, ErrRemoteFileDeletePending) {
		t.Fatal(err)
	}
	if _, err := s.PlanCommitRecovery(t.Context()); !errors.Is(err, ErrRemoteFileDeletePending) {
		t.Fatal(err)
	}
	if found, err := s.recoverCommit(t.Context(), wc); !found || !errors.Is(err, ErrRemoteFileDeletePending) {
		t.Fatal(found, err)
	}
	if err := s.tryCommitLocked(t.Context(), wc, false); !errors.Is(err, ErrRemoteFileDeletePending) {
		t.Fatal(err)
	}
	if err := s.WithRemoteFileDeleteMutation(t.Context(), wc, true, func() error {
		if s.wcOpMu.TryLock() {
			t.Fatal("delete recovery escaped real writer")
		}
		return nil
	}); err != nil || !HasPendingRemoteFileDelete(wc) {
		t.Fatal(err)
	}
	pendingRemoteTree(t, wc)
	if err := s.WithRemoteFileDeleteMutation(t.Context(), wc, true, call); !IsLocalMutationPending(err) {
		t.Fatal(err)
	}
}
