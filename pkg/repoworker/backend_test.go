package repoworker

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"testing"
	"time"
)

type effects struct {
	fsfs, publish int
	failPublish   bool
	failArchive   bool
	deleteSteps   []string
}

func (e *effects) CreateFSFS(context.Context, string, string) error { e.fsfs++; return nil }
func (e *effects) PublishAuthority(context.Context, string, string, string, string) error {
	e.publish++
	if e.failPublish {
		return errors.New("crash boundary")
	}
	return nil
}
func (e *effects) PrepareDelete(context.Context, string, string) error {
	e.deleteSteps = append(e.deleteSteps, "blocked")
	return nil
}
func (e *effects) WithdrawAuthority(context.Context, string, string) error {
	e.deleteSteps = append(e.deleteSteps, "withdrawn")
	return nil
}
func (e *effects) ArchiveAndDeleteFSFS(context.Context, string, string) (time.Time, error) {
	e.deleteSteps = append(e.deleteSteps, "archive")
	if e.failArchive {
		return time.Time{}, errors.New("archive boundary")
	}
	return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC), nil
}

func TestDurableBackendResumesRepositoryDeletionBoundaries(t *testing.T) {
	fx := &effects{failArchive: true}
	backend := &DurableBackend{Root: t.TempDir(), Effects: fx}
	operationID, realmID, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := backend.Delete(context.Background(), operationID, realmID, repoID); err == nil {
		t.Fatal("archive boundary failure missing")
	}
	fx.failArchive = false
	retainUntil, err := backend.Delete(context.Background(), operationID, realmID, repoID)
	if err != nil {
		t.Fatal(err)
	}
	wantSteps := []string{"blocked", "withdrawn", "archive", "archive"}
	if len(fx.deleteSteps) != len(wantSteps) {
		t.Fatalf("delete steps=%v", fx.deleteSteps)
	}
	for index := range wantSteps {
		if fx.deleteSteps[index] != wantSteps[index] {
			t.Fatalf("delete steps=%v, want %v", fx.deleteSteps, wantSteps)
		}
	}
	if retainUntil.IsZero() {
		t.Fatal("retention timestamp missing")
	}
	before := len(fx.deleteSteps)
	if replay, err := backend.Delete(context.Background(), operationID, realmID, repoID); err != nil || !replay.Equal(retainUntil) {
		t.Fatalf("delete replay=%s err=%v", replay, err)
	}
	if len(fx.deleteSteps) != before {
		t.Fatalf("completed delete replayed effects: %v", fx.deleteSteps)
	}
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
