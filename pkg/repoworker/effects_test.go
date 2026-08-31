package repoworker

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type publisher struct{ calls, deletes, prunes int }

func (p *publisher) Publish(context.Context, string, string, string, string, string) error {
	p.calls++
	return nil
}
func (p *publisher) Delete(context.Context, string, string) error { p.deletes++; return nil }
func (p *publisher) PruneAbandoned(context.Context, string, string) error {
	p.prunes++
	return nil
}
func TestServerEffectsCreatesFSFSIdempotently(t *testing.T) {
	bin, e := exec.LookPath("svnadmin")
	if e != nil {
		t.Skip("svnadmin unavailable")
	}
	pub := &publisher{}
	fx := ServerEffects{SVNAdmin: bin, RepositoriesRoot: filepath.Join(t.TempDir(), "repos"), DataAuthzFile: filepath.Join(t.TempDir(), "authz"), Authority: pub}
	id, op := uuid.NewString(), uuid.NewString()
	if e = fx.CreateFSFS(context.Background(), id, op); e != nil {
		t.Fatal(e)
	}
	if e = fx.CreateFSFS(context.Background(), id, op); e != nil {
		t.Fatal(e)
	}
	if !validRepo(filepath.Join(fx.RepositoriesRoot, id)) {
		t.Fatal("FSFS missing")
	}
}

func TestServerEffectsRollBackCreateAfterAuthorityFailure(t *testing.T) {
	bin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin unavailable")
	}
	pub := &publisher{}
	effects := ServerEffects{SVNAdmin: bin, RepositoriesRoot: filepath.Join(t.TempDir(), "repos"), DataAuthzFile: filepath.Join(t.TempDir(), "authz"), Authority: pub}
	repoID, operationID, realmID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := effects.CreateFSFS(context.Background(), repoID, operationID); err != nil {
		t.Fatal(err)
	}
	if err := effects.RollbackCreate(context.Background(), repoID, realmID); err != nil {
		t.Fatal(err)
	}
	if pub.deletes != 1 {
		t.Fatalf("authority deletes=%d", pub.deletes)
	}
	if _, err := os.Stat(filepath.Join(effects.RepositoriesRoot, repoID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("FSFS remains after rollback: %v", err)
	}
}

func TestServerEffectsAbandonedPruneRefusesRepositoryWhichAdvancedToR1(t *testing.T) {
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin unavailable")
	}
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn unavailable")
	}
	svnlook, err := exec.LookPath("svnlook")
	if err != nil {
		t.Skip("svnlook unavailable")
	}
	pub := &publisher{}
	effects := ServerEffects{
		SVNAdmin: svnadmin, SVNLook: svnlook, RepositoriesRoot: filepath.Join(t.TempDir(), "repos"),
		DataAuthzFile: filepath.Join(t.TempDir(), "authz"), Authority: pub,
	}
	repoID, operationID, realmID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := effects.CreateFSFS(context.Background(), repoID, operationID); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(effects.RepositoriesRoot, repoID)
	if output, err := exec.Command(svn, "mkdir", "file://"+filepath.ToSlash(repo)+"/late", "-m", "late initial import").CombinedOutput(); err != nil {
		t.Fatalf("create r1 fixture: %v: %s", err, output)
	}
	err = effects.PruneAbandonedCreate(context.Background(), repoID, realmID, operationID)
	if err == nil || !strings.Contains(err.Error(), "advanced to r1") {
		t.Fatalf("r1 prune error=%v", err)
	}
	if pub.prunes != 0 {
		t.Fatalf("authority was withdrawn for r1 repository: %d", pub.prunes)
	}
	if !validRepo(repo) {
		t.Fatal("r1 repository was removed")
	}
	hook := filepath.Join(repo, "hooks", "pre-commit")
	backup := hook + ".filees-delete-" + operationID + ".original"
	if _, err := os.Lstat(hook); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("commit blocker survived refused prune: %v", err)
	}
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("commit-hook backup survived refused prune: %v", err)
	}
}
