package commit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
)

func pendingRemoteTree(t *testing.T, wc string) {
	t.Helper()
	dir := filepath.Join(wc, ".filees", "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, RemoteTreePendingName), []byte("corrupt evidence also fences"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteTreePendingFencesWritersAndReleaseRecovery(t *testing.T) {
	wc := t.TempDir()
	s := &Service{Cli: &statusClient{entries: []client.StatusEntry{{Path: "file", Item: "normal", Props: "none"}}}}
	s.BindWorkingCopy("repo", wc)
	pendingRemoteTree(t, wc)
	called := false
	if HasPendingFileRelease(wc) || !HasPendingLocalMutation(wc) {
		t.Fatal("tree/release fences conflated")
	}
	if err := s.WithWorkingCopyWrite(t.Context(), wc, func() error { called = true; return nil }); !errors.Is(err, ErrRemoteTreePending) {
		t.Fatal(err)
	}
	if err := s.WithLocalStatus(t.Context(), wc, []string{"file"}, func([]client.StatusEntry) error { called = true; return nil }); !errors.Is(err, ErrRemoteTreePending) {
		t.Fatal(err)
	}
	if found, err := s.recoverCommit(t.Context(), wc); !found || !errors.Is(err, ErrRemoteTreePending) {
		t.Fatal(found, err)
	}
	if err := s.tryCommitLocked(t.Context(), wc, false); !errors.Is(err, ErrRemoteTreePending) {
		t.Fatal(err)
	}
	if _, err := s.PlanCommitRecovery(t.Context()); !errors.Is(err, ErrRemoteTreePending) {
		t.Fatal(err)
	}
	for _, recovery := range []bool{false, true} {
		if err := s.WithFileRelease(t.Context(), wc, "file", recovery, func() error { called = true; return nil }); !errors.Is(err, ErrRemoteTreePending) {
			t.Fatal(err)
		}
	}
	if err := s.WithRemoteTreeMutation(t.Context(), wc, false, func() error { called = true; return nil }); !errors.Is(err, ErrRemoteTreePending) {
		t.Fatal(err)
	}
	if called || !HasPendingRemoteTree(wc) {
		t.Fatal("ordinary writer ran or cleared evidence")
	}
	if err := s.WithRemoteTreeMutation(t.Context(), wc, true, func() error {
		if s.wcOpMu.TryLock() {
			t.Fatal("recovery escaped writer guard")
		}
		return nil
	}); err != nil || !HasPendingRemoteTree(wc) {
		t.Fatal(err)
	}
}

func TestRemoteTreeWriterAdmissionRequiresIdleAndCorrectFence(t *testing.T) {
	wc := t.TempDir()
	s := &Service{Cli: &statusClient{}}
	s.BindWorkingCopy("repo", wc)
	if err := s.WithRemoteTreeMutation(t.Context(), wc, true, func() error { t.Fatal("recovery without evidence"); return nil }); !IsLocalMutationPending(err) {
		t.Fatal(err)
	}
	s.staging = map[string]*stageItem{"unrelated": {}}
	if err := s.WithRemoteTreeMutation(t.Context(), wc, false, func() error { t.Fatal("staged work admitted"); return nil }); !errors.Is(err, ErrLocalStatusBusy) {
		t.Fatal(err)
	}
	s.staging = nil
	pendingFileRelease(t, wc)
	if err := s.WithRemoteTreeMutation(t.Context(), wc, false, func() error { t.Fatal("release fence bypassed"); return nil }); !errors.Is(err, ErrFileReleasePending) {
		t.Fatal(err)
	}
	if err := s.WithRemoteTreeMutation(t.Context(), wc, true, func() error { t.Fatal("release recovery stolen"); return nil }); !errors.Is(err, ErrFileReleasePending) {
		t.Fatal(err)
	}
}
