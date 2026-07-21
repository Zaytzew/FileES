package repoworker

import (
	"context"
	"github.com/google/uuid"
	"os/exec"
	"path/filepath"
	"testing"
)

type publisher struct{ calls int }

func (p *publisher) Publish(context.Context, string, string, string, string) error {
	p.calls++
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
