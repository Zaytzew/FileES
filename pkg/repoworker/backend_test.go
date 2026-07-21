package repoworker

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"testing"
)

type effects struct {
	fsfs, publish int
	failPublish   bool
}

func (e *effects) CreateFSFS(context.Context, string, string) error { e.fsfs++; return nil }
func (e *effects) PublishAuthority(context.Context, string, string, string, string) error {
	e.publish++
	if e.failPublish {
		return errors.New("crash boundary")
	}
	return nil
}
func TestDurableBackendResumesAfterFSFS(t *testing.T) {
	fx := &effects{failPublish: true}
	b := &DurableBackend{Root: t.TempDir(), URLPrefix: "svn+ssh://_filees-client@example/repos/", Effects: fx}
	op, realm := uuid.NewString(), uuid.NewString()
	if _, e := b.Create(context.Background(), op, realm, "Docs"); e == nil {
		t.Fatal("failure missing")
	}
	fx.failPublish = false
	r, e := b.Create(context.Background(), op, realm, "Docs")
	if e != nil {
		t.Fatal(e)
	}
	if fx.fsfs != 1 || fx.publish != 2 || r.RepoID == "" {
		t.Fatalf("fsfs=%d publish=%d repo=%+v", fx.fsfs, fx.publish, r)
	}
}
