package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
)

func TestWorkingCopyRecoveryRetriesBeforeUnchangedHeadShortcut(t *testing.T) {
	wc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wc, ".filees", "state"), 0700); err != nil {
		t.Fatal(err)
	}
	c := &revisionClient{local: 51, remote: 51, cleanupErr: errors.New("cloud file access denied")}
	var status *contract.WorkingCopyRecoveryStatus
	s := &Service{Cli: c, Anchor: true, RepoURL: "svn://fixture/repo", OnWorkingCopyRecovery: func(p *contract.WorkingCopyRecoveryStatus) { status = p }}
	s.BindWorkingCopy("repo", wc)
	if _, err := s.RecoverWorkingCopy(t.Context(), wc); err == nil || !HasPendingWorkingCopyRecovery(wc) || status == nil || status.Phase != "blocked" {
		t.Fatal("failure was lost", err, status)
	}
	if err := s.tryCommitMode(t.Context(), wc, true); !errors.Is(err, ErrWorkingCopyRecoveryPending) {
		t.Fatal("publication crossed recovery fence", err)
	}
	// New service simulates a restart. Only the durable marker remains.
	c.cleanupErr = nil
	s = &Service{Cli: c, Anchor: true, RepoURL: "svn://fixture/repo", OnWorkingCopyRecovery: func(p *contract.WorkingCopyRecoveryStatus) { status = p }}
	s.BindWorkingCopy("repo", wc)
	s.pollOnce(t.Context(), wc, filepath.Join(wc, ".filees", "state", "head.rev"))
	if c.cleanupCalls != 2 || c.update != 1 || HasPendingWorkingCopyRecovery(wc) || status != nil {
		t.Fatal("unchanged HEAD hid unfinished work", c.cleanupCalls, c.update, status)
	}
}

func TestWorkingCopyRecoveryCancellationKeepsFence(t *testing.T) {
	wc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wc, ".filees", "state"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	c := &revisionClient{updateHook: func() error { cancel(); return context.Canceled }}
	var status *contract.WorkingCopyRecoveryStatus
	s := &Service{Cli: c, Anchor: true, OnWorkingCopyRecovery: func(p *contract.WorkingCopyRecoveryStatus) { status = p }}
	_, err := s.RecoverWorkingCopy(ctx, wc)
	if !errors.Is(err, context.Canceled) || !HasPendingLocalMutation(wc) || status == nil || status.Phase != "waiting" {
		t.Fatal(err, status)
	}
}

func TestWorkingCopyRecoveryCannotLoseFenceOnPersistenceFailure(t *testing.T) {
	wc := t.TempDir()
	s := &Service{Cli: &revisionClient{}, Anchor: true}
	if _, err := s.RecoverWorkingCopy(t.Context(), wc); err == nil {
		t.Fatal("missing state directory admitted")
	}
	if !s.workingRecoveryRequired.Load() {
		t.Fatal("persistence error cleared volatile fence")
	}
	if err := s.tryCommitMode(t.Context(), wc, true); !errors.Is(err, ErrWorkingCopyRecoveryPending) {
		t.Fatal(err)
	}
}

func TestWorkingCopyRecoveryDoesNotFenceTheCommitItAwaits(t *testing.T) {
	wc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wc, ".filees", "state"), 0700); err != nil {
		t.Fatal(err)
	}
	c := &revisionClient{status: []client.StatusEntry{{Path: "user-deleted.txt", Item: "missing"}}}
	s := &Service{Cli: c, Anchor: true}
	_, err := s.RecoverWorkingCopy(t.Context(), wc)
	if !IsWorkingCopyRecoveryWaiting(err) || HasPendingWorkingCopyRecovery(wc) || s.workingRecoveryRequired.Load() || c.update != 0 {
		t.Fatal("cleaned queue blocked local publication", err, c.update)
	}
}

type projectionCompletionClient struct {
	*revisionClient
	complete func() error
}

func (c *projectionCompletionClient) CompleteWorkingCopyProjection(context.Context, string) error {
	return c.complete()
}

func TestWorkingCopyRecoveryKeepsFenceUntilVirtualViewIsRestored(t *testing.T) {
	wc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wc, ".filees", "state"), 0700); err != nil {
		t.Fatal(err)
	}
	restored, cleared := false, false
	c := &projectionCompletionClient{revisionClient: &revisionClient{}, complete: func() error {
		if !HasPendingWorkingCopyRecovery(wc) {
			t.Fatal("view restored outside pending fence")
		}
		if !restored {
			return errors.New("local restore obstruction")
		}
		return nil
	}}
	s := &Service{Cli: c, Anchor: true, OnWorkingCopyRecovery: func(p *contract.WorkingCopyRecoveryStatus) {
		if p == nil {
			cleared = true
		}
	}}
	if _, err := s.RecoverWorkingCopy(t.Context(), wc); err == nil || cleared || !HasPendingWorkingCopyRecovery(wc) {
		t.Fatal("premature completion", err)
	}
	restored = true
	if _, err := s.RecoverWorkingCopy(t.Context(), wc); err != nil || !cleared || HasPendingWorkingCopyRecovery(wc) {
		t.Fatal("restored view did not finish", err)
	}
}
