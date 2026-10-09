package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
	"filees/pkg/watcher"
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

func TestRemoteTreeMoveUsesRealWriterAndScannerCallbacks(t *testing.T) {
	wc := t.TempDir()
	s := &Service{Cli: &statusClient{}}
	s.BindWorkingCopy("repo", wc)
	called := 0
	if err := s.WithRemoteTreeMove(t.Context(), wc, "old", "new", nil, func(*watcher.TreeMoveSnapshot) error { called++; return nil }); !errors.Is(err, errors.ErrUnsupported) || called != 0 {
		t.Fatal(err, called)
	}
	p := &watcher.TreeMoveSnapshot{WC: wc, Source: "old", Destination: "new"}
	s.FenceTreeMove = func(ctx context.Context, source, destination string, operation func(*watcher.TreeMoveSnapshot) error) error {
		if s.wcOpMu.TryLock() {
			s.wcOpMu.Unlock()
			t.Fatal("scanner outside writer fence")
		}
		if source != "old" || destination != "new" {
			t.Fatal("selection widened")
		}
		return operation(p)
	}
	if err := s.WithRemoteTreeMove(t.Context(), wc, "old", "new", nil, func(got *watcher.TreeMoveSnapshot) error {
		if got != p {
			t.Fatal("lost observations")
		}
		called++
		return nil
	}); err != nil || called != 1 {
		t.Fatal(err, called)
	}
	pendingRemoteTree(t, wc)
	s.RecoverTreeMove = func(ctx context.Context, got *watcher.TreeMoveSnapshot, inspect func() error) error {
		if s.wcOpMu.TryLock() {
			s.wcOpMu.Unlock()
			t.Fatal("recovery outside writer fence")
		}
		if got != p {
			t.Fatal("replaced persisted observations")
		}
		return inspect()
	}
	if err := s.WithRemoteTreeMove(t.Context(), wc, "old", "new", p, func(got *watcher.TreeMoveSnapshot) error { called++; return nil }); err != nil || called != 2 || !HasPendingRemoteTree(wc) {
		t.Fatal(err, called)
	}
	for _, selection := range [][2]string{{"foreign", "new"}, {"old", "foreign"}} {
		if err := s.WithRemoteTreeMove(t.Context(), wc, selection[0], selection[1], p, func(*watcher.TreeMoveSnapshot) error { t.Fatal("changed recovery selection entered"); return nil }); err == nil {
			t.Fatal("changed selection admitted")
		}
	}
	s.RecoverTreeMove = nil
	if err := s.WithRemoteTreeMove(t.Context(), wc, "old", "new", p, func(*watcher.TreeMoveSnapshot) error { t.Fatal("missing scanner bypassed"); return nil }); !errors.Is(err, errors.ErrUnsupported) {
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
