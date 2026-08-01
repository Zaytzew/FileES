package repoworker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func deletionFixture(t *testing.T, retentionDays int, now time.Time) (ServerEffects, string, string) {
	t.Helper()
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin unavailable")
	}
	root := filepath.Join(t.TempDir(), "repositories")
	archive := filepath.Join(t.TempDir(), "deleted")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	repoID := uuid.NewString()
	repo := filepath.Join(root, repoID)
	if output, err := exec.Command(svnadmin, "create", repo).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, output)
	}
	effects := ServerEffects{
		SVNAdmin: svnadmin, RepositoriesRoot: root,
		DeletionArchiveRoot: archive, DeletionRetentionDays: retentionDays,
		Now: func() time.Time { return now },
	}
	return effects, repoID, repo
}

func TestArchiveAndDeleteFSFSRetentionZeroLeavesNoRecoveryCopy(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	effects, repoID, repo := deletionFixture(t, 0, now)
	retainUntil, err := effects.ArchiveAndDeleteFSFS(context.Background(), repoID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if !retainUntil.Equal(now) {
		t.Fatalf("retain until=%s, want immediate deletion at %s", retainUntil, now)
	}
	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		t.Fatalf("FSFS survived panic deletion: %v", err)
	}
	if _, err := os.Stat(effects.DeletionArchiveRoot); !os.IsNotExist(err) {
		t.Fatalf("retention zero created a server-side archive: %v", err)
	}
}

func TestArchiveAndDeleteFSFSResumesPartiallyRemovedExactTree(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	effects, repoID, repo := deletionFixture(t, 0, now)
	if err := os.Remove(filepath.Join(repo, "format")); err != nil {
		t.Fatal(err)
	}
	if _, err := effects.ArchiveAndDeleteFSFS(context.Background(), repoID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(repo); !os.IsNotExist(err) {
		t.Fatalf("partially removed repository tree survived retry: %v", err)
	}
}

func TestArchiveAndDeleteFSFSDumpsVerifiesThenReaps(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	effects, repoID, repo := deletionFixture(t, 7, now)
	operationID := uuid.NewString()
	retainUntil, err := effects.ArchiveAndDeleteFSFS(context.Background(), repoID, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(7 * 24 * time.Hour); !retainUntil.Equal(want) {
		t.Fatalf("retain until=%s, want %s", retainUntil, want)
	}
	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		t.Fatalf("FSFS survived verified dump: %v", err)
	}
	base := repoID + "-" + operationID
	dump := filepath.Join(effects.DeletionArchiveRoot, base+".svndump")
	meta := filepath.Join(effects.DeletionArchiveRoot, base+".json")
	if info, err := os.Stat(dump); err != nil || info.Size() == 0 {
		t.Fatalf("retained dump missing or empty: info=%v err=%v", info, err)
	}
	descriptor, descriptorUntil, found, err := DeletionRecoveryArchive(effects.DeletionArchiveRoot, repoID, operationID)
	if err != nil || !found || descriptor.RepoID != repoID || descriptor.Size <= 0 || descriptor.SHA256 == "" || !descriptorUntil.Equal(retainUntil) {
		t.Fatalf("recovery descriptor=%+v until=%s found=%v err=%v", descriptor, descriptorUntil, found, err)
	}
	if _, err := os.Stat(meta); err != nil {
		t.Fatalf("retained dump metadata: %v", err)
	}
	// The durable retry path must accept the verified archive and remain
	// idempotent after the FSFS tree is already gone.
	if second, err := effects.ArchiveAndDeleteFSFS(context.Background(), repoID, operationID); err != nil || !second.Equal(retainUntil) {
		t.Fatalf("idempotent deletion=%s err=%v", second, err)
	}
	if removed, err := ReapDeletionArchives(effects.DeletionArchiveRoot, retainUntil.Add(-time.Second)); err != nil || removed != 0 {
		t.Fatalf("premature reaper removed=%d err=%v", removed, err)
	}
	if removed, err := ReapDeletionArchives(effects.DeletionArchiveRoot, retainUntil); err != nil || removed != 1 {
		t.Fatalf("expired reaper removed=%d err=%v", removed, err)
	}
	for _, path := range []string{dump, meta} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expired archive survived at %s: %v", path, err)
		}
	}
}

func TestPrepareDeleteBlocksCommitsAndPreservesPriorHook(t *testing.T) {
	effects, repoID, repo := deletionFixture(t, 1, time.Now())
	operationID := uuid.NewString()
	hook := filepath.Join(repo, "hooks", "pre-commit")
	originalBody := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(hook, originalBody, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := effects.PrepareDelete(context.Background(), repoID, operationID); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(replacement), operationID) {
		t.Fatalf("blocking hook lacks operation marker: %q", replacement)
	}
	backup := hook + ".filees-delete-" + operationID + ".original"
	if got, err := os.ReadFile(backup); err != nil || string(got) != string(originalBody) {
		t.Fatalf("prior hook backup=%q err=%v", got, err)
	}
	if err := effects.PrepareDelete(context.Background(), repoID, operationID); err != nil {
		t.Fatalf("idempotent prepare: %v", err)
	}
}

func TestRestoreDeleteRestoresPriorHookAndIsIdempotent(t *testing.T) {
	effects, repoID, repo := deletionFixture(t, 1, time.Now())
	operationID := uuid.NewString()
	hook := filepath.Join(repo, "hooks", "pre-commit")
	backup := hook + ".filees-delete-" + operationID + ".original"
	originalBody := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(hook, originalBody, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := effects.PrepareDelete(context.Background(), repoID, operationID); err != nil {
		t.Fatal(err)
	}
	if err := effects.RestoreDelete(context.Background(), repoID, operationID); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(hook); err != nil || string(got) != string(originalBody) {
		t.Fatalf("restored hook=%q err=%v", got, err)
	}
	if _, err := os.Lstat(backup); !os.IsNotExist(err) {
		t.Fatalf("delete backup survived restoration: %v", err)
	}
	if err := effects.RestoreDelete(context.Background(), repoID, operationID); err != nil {
		t.Fatalf("idempotent restore: %v", err)
	}
}

func TestRestoreDeleteWithoutPriorHookRemovesOwnBlocker(t *testing.T) {
	effects, repoID, repo := deletionFixture(t, 1, time.Now())
	operationID := uuid.NewString()
	hook := filepath.Join(repo, "hooks", "pre-commit")
	if err := effects.PrepareDelete(context.Background(), repoID, operationID); err != nil {
		t.Fatal(err)
	}
	if err := effects.RestoreDelete(context.Background(), repoID, operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(hook); !os.IsNotExist(err) {
		t.Fatalf("own blocker survived restoration: %v", err)
	}
	if err := effects.RestoreDelete(context.Background(), repoID, operationID); err != nil {
		t.Fatalf("idempotent restore without prior hook: %v", err)
	}
}

func TestRestoreDeleteRecoversHookMovedBeforeBlockerInstall(t *testing.T) {
	effects, repoID, repo := deletionFixture(t, 1, time.Now())
	operationID := uuid.NewString()
	hook := filepath.Join(repo, "hooks", "pre-commit")
	backup := hook + ".filees-delete-" + operationID + ".original"
	originalBody := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(hook, originalBody, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(hook, backup); err != nil {
		t.Fatal(err)
	}
	if err := effects.RestoreDelete(context.Background(), repoID, operationID); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(hook); err != nil || string(got) != string(originalBody) {
		t.Fatalf("partially moved hook=%q err=%v", got, err)
	}
}

func TestRestoreDeleteRefusesArtifactsOwnedByAnotherOperation(t *testing.T) {
	t.Run("blocker", func(t *testing.T) {
		effects, repoID, repo := deletionFixture(t, 1, time.Now())
		ownerOperation := uuid.NewString()
		hook := filepath.Join(repo, "hooks", "pre-commit")
		if err := effects.PrepareDelete(context.Background(), repoID, ownerOperation); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(hook)
		if err != nil {
			t.Fatal(err)
		}
		if err := effects.RestoreDelete(context.Background(), repoID, uuid.NewString()); err == nil {
			t.Fatal("foreign blocker was accepted")
		}
		if after, err := os.ReadFile(hook); err != nil || string(after) != string(before) {
			t.Fatalf("foreign blocker changed: %q err=%v", after, err)
		}
	})

	t.Run("backup", func(t *testing.T) {
		effects, repoID, repo := deletionFixture(t, 1, time.Now())
		ownerOperation := uuid.NewString()
		hook := filepath.Join(repo, "hooks", "pre-commit")
		originalBody := []byte("#!/bin/sh\nexit 0\n")
		if err := os.WriteFile(hook, originalBody, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := effects.PrepareDelete(context.Background(), repoID, ownerOperation); err != nil {
			t.Fatal(err)
		}
		backup := hook + ".filees-delete-" + ownerOperation + ".original"
		beforeHook, err := os.ReadFile(hook)
		if err != nil {
			t.Fatal(err)
		}
		if err := effects.RestoreDelete(context.Background(), repoID, uuid.NewString()); err == nil {
			t.Fatal("foreign backup was accepted")
		}
		if after, err := os.ReadFile(hook); err != nil || string(after) != string(beforeHook) {
			t.Fatalf("hook changed beside foreign backup: %q err=%v", after, err)
		}
		if after, err := os.ReadFile(backup); err != nil || string(after) != string(originalBody) {
			t.Fatalf("foreign backup changed: %q err=%v", after, err)
		}
	})
}

func TestForeignDeleteCannotInstallCommitBlockerBeforeAuthorityCheck(t *testing.T) {
	effects, repoID, repo := deletionFixture(t, 7, time.Now())
	ownerRealm, foreignRealm := uuid.NewString(), uuid.NewString()
	serviceRoot := t.TempDir()
	recordPath, err := repositoryRecordPath(serviceRoot, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(recordPath, repositoryRecord{
		Schema: RepositorySchema, RepoID: repoID, OwnerRealmID: ownerRealm,
		DisplayName: "Owned", URL: "svn+ssh://_filees-data@example/" + repoID,
		State: "active", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	effects.Authority = ServicePublisher{ServiceWC: serviceRoot, DataAuthzFile: filepath.Join(serviceRoot, "repositories.authz"), Runner: &publishRunner{}}
	backend := &DurableBackend{Root: t.TempDir(), Effects: effects}
	operationID := uuid.NewString()
	hook := filepath.Join(repo, "hooks", "pre-commit")
	original := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(hook, original, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Delete(context.Background(), operationID, foreignRealm, repoID); err == nil {
		t.Fatal("foreign realm deletion was accepted")
	}
	if got, err := os.ReadFile(hook); err != nil || string(got) != string(original) {
		t.Fatalf("foreign deletion changed pre-commit hook: %q err=%v", got, err)
	}
	backup := hook + ".filees-delete-" + operationID + ".original"
	if _, err := os.Lstat(backup); !os.IsNotExist(err) {
		t.Fatalf("foreign deletion parked the owner's hook: %v", err)
	}
}
