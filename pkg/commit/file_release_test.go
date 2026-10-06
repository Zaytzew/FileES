package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
)

func pendingFileRelease(t *testing.T, wc string) {
	t.Helper()
	dir := filepath.Join(wc, ".filees", "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileReleasePendingName), []byte("even corrupt evidence fences writers"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPendingFileReleaseFencesWritersAndPublicationRecovery(t *testing.T) {
	wc := t.TempDir()
	s := &Service{Cli: &statusClient{entries: []client.StatusEntry{{Path: "file", Item: "normal", Props: "none"}}}}
	s.BindWorkingCopy("repo", wc)
	pendingFileRelease(t, wc)
	called := false
	if err := s.WithWorkingCopyWrite(t.Context(), wc, func() error { called = true; return nil }); !errors.Is(err, ErrFileReleasePending) {
		t.Fatal(err)
	}
	if err := s.WithLocalStatus(t.Context(), wc, []string{"file"}, func([]client.StatusEntry) error { called = true; return nil }); !errors.Is(err, ErrFileReleasePending) {
		t.Fatal(err)
	}
	if found, err := s.recoverCommit(t.Context(), wc); !found || !errors.Is(err, ErrFileReleasePending) {
		t.Fatal(found, err)
	}
	if err := s.tryCommitLocked(t.Context(), wc, false); !errors.Is(err, ErrFileReleasePending) {
		t.Fatal(err)
	}
	if _, err := s.PlanCommitRecovery(t.Context()); !errors.Is(err, ErrFileReleasePending) {
		t.Fatal(err)
	}
	if called || !HasPendingFileRelease(wc) {
		t.Fatal("writer ran or fence disappeared")
	}
}

func TestFileReleaseUsesWriterAndScannerFenceAndExactAdmission(t *testing.T) {
	wc := t.TempDir()
	s := &Service{Cli: &statusClient{entries: []client.StatusEntry{{Path: "file", Item: "normal", Props: "none"}}}}
	s.BindWorkingCopy("repo", wc)
	fenced, called := false, false
	s.FenceFileRelease = func(ctx context.Context, rel string, op func() error) error {
		fenced = true
		if rel != "file" || s.wcOpMu.TryLock() {
			t.Fatal("missing real writer fence")
		}
		return op()
	}
	if err := s.WithFileRelease(t.Context(), wc, "file", false, func() error { called = true; return nil }); err != nil || !called || !fenced {
		t.Fatal(err, called, fenced)
	}
	for _, path := range []string{"../file", ".filees/private", "file\nother", "."} {
		if s.WithFileRelease(t.Context(), wc, path, false, func() error { t.Fatal("bad path admitted"); return nil }) == nil {
			t.Fatal("invalid path accepted")
		}
	}
	s.staging = map[string]*stageItem{"file": {}}
	if err := s.WithFileRelease(t.Context(), wc, "file", false, func() error { t.Fatal("staged work admitted"); return nil }); !errors.Is(err, ErrLocalStatusBusy) {
		t.Fatal(err)
	}
	s.staging = nil
	s.staging = map[string]*stageItem{"moved": {OldRel: "file/child"}}
	if err := s.WithFileRelease(t.Context(), wc, "file", false, func() error { t.Fatal("staged move admitted"); return nil }); !errors.Is(err, ErrLocalStatusBusy) {
		t.Fatal(err)
	}
	s.staging = nil
	pendingFileRelease(t, wc)
	if err := s.WithFileRelease(t.Context(), wc, "file", false, func() error { t.Fatal("new release during recovery"); return nil }); !errors.Is(err, ErrFileReleasePending) {
		t.Fatal(err)
	}
	if err := s.WithFileRelease(t.Context(), wc, "file", true, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !HasPendingFileRelease(wc) {
		t.Fatal("internal recovery callback cleared evidence implicitly")
	}
}

func TestFileReleaseCompletionFollowsDurableScannerAcknowledgement(t *testing.T) {
	wc := t.TempDir()
	s := &Service{Cli: &statusClient{entries: []client.StatusEntry{{Path: "file", Item: "normal", Props: "none"}}}}
	s.BindWorkingCopy("repo", wc)
	acknowledged := false
	s.FenceFileRelease = func(_ context.Context, _ string, op func() error) error {
		if err := op(); err != nil {
			return err
		}
		acknowledged = true
		return nil
	}
	completed := false
	if err := s.WithFileRelease(t.Context(), wc, "file", false, func() error { return nil }, func() error {
		if !acknowledged || s.wcOpMu.TryLock() {
			t.Fatal("completion precedes ack or left writer guard")
		}
		completed = true
		return nil
	}); err != nil || !completed {
		t.Fatal(err, completed)
	}
	ackFailure := errors.New("durable scanner write refused")
	s.FenceFileRelease = func(context.Context, string, func() error) error { return ackFailure }
	if err := s.WithFileRelease(t.Context(), wc, "file", false, func() error { return nil }, func() error { t.Fatal("completed without durable ack"); return nil }); !errors.Is(err, ackFailure) {
		t.Fatal(err)
	}
}
