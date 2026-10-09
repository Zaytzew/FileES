package commit

import (
	"context"
	"errors"
	"filees/pkg/watcher"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileDeleteCoordinatorWriterScannerStorageAndRetirementOrder(t *testing.T) {
	wc := t.TempDir()
	s := &Service{Cli: &statusClient{}}
	s.BindWorkingCopy("repo", wc)
	p := &watcher.FileDeleteSnapshot{WC: wc, Source: "selected"}
	var steps []string
	s.CheckPublication = func(context.Context) error { steps = append(steps, "capacity"); return nil }
	s.FenceFileDelete = func(ctx context.Context, source string, op func(*watcher.FileDeleteSnapshot) error) error {
		if s.wcOpMu.TryLock() {
			t.Fatal("preparation escaped writer")
		}
		steps = append(steps, "prepare-scanner")
		return op(p)
	}
	if err := s.WithRemoteFileDelete(t.Context(), wc, "selected", nil, func(got *watcher.FileDeleteSnapshot) error {
		if got != p {
			t.Fatal("lost observations")
		}
		steps = append(steps, "intent")
		if err := os.MkdirAll(filepath.Join(wc, ".filees", "state"), 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(wc, ".filees", "state", RemoteFileDeletePendingName), []byte("private proof"), 0600)
	}, nil); err != nil {
		t.Fatal(err)
	}
	s.RecoverFileDelete = func(ctx context.Context, got *watcher.FileDeleteSnapshot, op func() error) error {
		if got != p || s.wcOpMu.TryLock() {
			t.Fatal("recovery escaped writer/observations")
		}
		steps = append(steps, "recovery-scanner")
		if err := op(); err != nil {
			return err
		}
		steps = append(steps, "durable-ack")
		return nil
	}
	if err := s.WithRemoteFileDelete(t.Context(), wc, "selected", p, func(*watcher.FileDeleteSnapshot) error { steps = append(steps, "proof"); return nil }, func() error {
		if s.wcOpMu.TryLock() {
			t.Fatal("retirement escaped writer")
		}
		steps = append(steps, "retire")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(steps, ","); got != "capacity,prepare-scanner,intent,capacity,recovery-scanner,proof,durable-ack,retire" {
		t.Fatal(got)
	}
	blocked := errors.New("capacity HOLD")
	s.CheckPublication = func(context.Context) error { return blocked }
	if err := s.WithRemoteFileDelete(t.Context(), wc, "selected", p, func(*watcher.FileDeleteSnapshot) error { t.Fatal("capacity bypassed"); return nil }, func() error { t.Fatal("capacity retired fence"); return nil }); !errors.Is(err, blocked) {
		t.Fatal(err)
	}
}

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
