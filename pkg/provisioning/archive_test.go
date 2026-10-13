package provisioning

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func archiveFixture(t *testing.T) (*Store, Operation) {
	t.Helper()
	store := newTestStore(t, filepath.Join(t.TempDir(), "journal"))
	source := filepath.Join(t.TempDir(), "project.dmp")
	if err := os.WriteFile(source, []byte("SVN-fs-dump-format-version: 2\n\nfixture"), 0600); err != nil {
		t.Fatal(err)
	}
	op, err := store.CreateValidatedArchive(uuid.NewString(), "client", filepath.Join(t.TempDir(), "target"), "Project", source)
	if err != nil {
		t.Fatal(err)
	}
	return store, op
}

func TestArchiveSnapshotPersistsAndResumesWithoutOriginal(t *testing.T) {
	s, op := archiveFixture(t)
	op, err := s.PrepareArchive(t.Context(), op.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Archive.Digest == "" || !op.ArchivePending() {
		t.Fatalf("snapshot=%+v", op)
	}
	if _, err := os.Stat(op.Archive.Target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("target touched while staging")
	}
	if err := os.Remove(op.Archive.Source); err != nil {
		t.Fatal(err)
	}
	reopened := newTestStore(t, s.root)
	if _, err := reopened.PrepareArchive(t.Context(), op.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(op.LocalPath, archiveCarrier), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.PrepareArchive(t.Context(), op.OperationID); err == nil {
		t.Fatal("changed snapshot accepted")
	}
}

func TestArchivePreparationRefusesChangedInvalidAndCanceledSource(t *testing.T) {
	for _, kind := range []string{"changed", "invalid", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			s, op := archiveFixture(t)
			ctx := t.Context()
			if kind == "changed" {
				_ = os.WriteFile(op.Archive.Source, []byte("different"), 0600)
			}
			if kind == "invalid" {
				_ = os.WriteFile(op.Archive.Source, []byte("not an archive"), 0600)
				s, _ = NewStore(filepath.Join(t.TempDir(), "state"))
				var err error
				op, err = s.CreateValidatedArchive(uuid.NewString(), "client", op.Archive.Target, "Project", op.Archive.Source)
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := s.PrepareArchive(ctx, op.OperationID); err == nil {
				t.Fatal("unsafe input prepared")
			}
			if _, err := os.Stat(op.Archive.Target); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("target mutated")
			}
		})
	}
}

func TestArchiveSourceMustBeExternalAndGenerationMustBeConfirmed(t *testing.T) {
	s, op := archiveFixture(t)
	if _, err := s.CreateValidatedArchive(op.OperationID, "client", filepath.Join(t.TempDir(), "other"), "Project", op.Archive.Source); err == nil {
		t.Fatal("operation rebound to another target")
	}
	if _, err := s.CreateValidatedArchive(uuid.NewString(), "client", filepath.Dir(op.Archive.Source), "Wrong", op.Archive.Source); err == nil {
		t.Fatal("source inside target accepted")
	}
	if _, err := s.CompleteArchive(op.OperationID); err == nil {
		t.Fatal("unloaded archive completed")
	}
	if _, err := s.MarkArchiveLoaded(op.OperationID, uuid.NewString()); err == nil {
		t.Fatal("unpublished carrier marked loaded")
	}
}

func TestArchiveCarrierImportRestartsWithoutSecondCommit(t *testing.T) {
	s, op := archiveFixture(t)
	if _, err := s.PrepareArchive(t.Context(), op.OperationID); err != nil {
		t.Fatal(err)
	}
	transport := &fakeControlExchange{failInitialOnce: true}
	svn := &fakeInitialSVN{root: op.LocalPath, items: map[string]string{}}
	orchestrator := Orchestrator{Store: s, Control: transport, SVN: svn, Limits: ImportLimits{MaxBatchFiles: 100, MaxBatchBytes: 1}}
	if _, err := orchestrator.RunCreate(t.Context(), op.OperationID); err == nil {
		t.Fatal("lost reply not exercised")
	}
	orchestrator.Store = newTestStore(t, s.root)
	op, err := orchestrator.RunCreate(t.Context(), op.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StateActive || !op.ArchivePending() || len(svn.commits) != 1 || op.Paths != 1 || op.Revision != 1 {
		t.Fatalf("carrier boundary=%+v commits=%v", op, svn.commits)
	}
	if _, err := os.Stat(op.Archive.Target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("carrier became user's working copy")
	}
	op, err = s.MarkArchiveLoaded(op.OperationID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	op, err = s.CompleteArchive(op.OperationID)
	if err != nil || op.ArchivePending() {
		t.Fatalf("completion=%+v err=%v", op, err)
	}
	if err := s.DiscardCompletedArchive(op.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(op.LocalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("private carrier left after completion")
	}
	if _, err := os.Stat(op.Archive.Source); err != nil {
		t.Fatal("external archive removed")
	}
}
