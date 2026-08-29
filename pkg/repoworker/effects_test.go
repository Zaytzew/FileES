package repoworker

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type publisher struct{ calls, deletes int }

func (p *publisher) Publish(context.Context, string, string, string, string, string) error {
	p.calls++
	return nil
}
func (p *publisher) Delete(context.Context, string, string) error { p.deletes++; return nil }
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
