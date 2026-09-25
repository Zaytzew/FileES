package repoworker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	control "filees/pkg/control/v1"

	"github.com/google/uuid"
)

type foreignOwnedEffects struct {
	*effects
	foreign error
}

func (e foreignOwnedEffects) CheckDeleteOwnership(string, string) error { return e.foreign }

// spot, 2026-09-25: files owned by root made svnadmin freeze fail on every
// retry, after the repository had already been blocked and withdrawn from the
// view. The check runs before any side effect, and the answer names the file.
func TestDeletionOfARepositoryWithAForeignOwnedFileChangesNothing(t *testing.T) {
	fx := &effects{}
	foreign := &RepositoryOwnershipError{Repo: "/var/filees/repositories/r", Path: "db/rep-cache.db", UID: 0, WantUID: 1006}
	backend := &DurableBackend{Root: t.TempDir(), Effects: foreignOwnedEffects{effects: fx, foreign: foreign}}
	operationID, realmID, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, err := backend.Delete(context.Background(), operationID, realmID, repoID)
	var got *RepositoryOwnershipError
	if !errors.As(err, &got) || got.Path != "db/rep-cache.db" {
		t.Fatalf("delete error = %v", err)
	}
	if len(fx.deleteSteps) != 0 {
		t.Fatalf("side effects before the ownership check: %v", fx.deleteSteps)
	}
	if !strings.Contains(foreign.Error(), "db/rep-cache.db") || !strings.Contains(foreign.Error(), "uid 0") || !strings.Contains(foreign.Error(), "chown -R") {
		t.Fatalf("message does not name the file, its owner and the fix: %s", foreign.Error())
	}

	// Corrected by the administrator: the same operation continues.
	backend.Effects = foreignOwnedEffects{effects: fx}
	if _, err := backend.Delete(context.Background(), operationID, realmID, repoID); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fx.deleteSteps, ","); got != "blocked,withdrawn,archive" {
		t.Fatalf("delete steps after correction = %s", got)
	}
}

type foreignOwnedBackend struct{ fakeBackend }

func (b *foreignOwnedBackend) Delete(context.Context, string, string, string) (time.Time, error) {
	return time.Time{}, &RepositoryOwnershipError{Repo: "/r", Path: "db/rep-cache.db", UID: 0, WantUID: 1006}
}

func TestWorkerReportsAForeignOwnedRepositoryInsteadOfRetrying(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	worker := &Worker{Backend: &foreignOwnedBackend{}, Store: store}
	session := Session{ClientID: "client-a", RealmID: uuid.NewString(), CanCreateRepositories: true}
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketDeleteRepository,
		session.ClientID, control.DeleteRepositoryPayload{RepoID: uuid.NewString()}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.Handle(context.Background(), session, ticket)
	if err != nil || result.Status != control.ResultError || result.Error == nil || result.Error.Code != "DELETE_REPOSITORY_OWNERSHIP" || !strings.Contains(result.Error.Message, "db/rep-cache.db") {
		t.Fatalf("delete result=%+v err=%v", result, err)
	}
}
