package channel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestStateReaderNeedsNoSecretsOrWritesAndChecksAuthority(t *testing.T) {
	store, declaration, realm := fixture(t)
	id := uuid.NewString()
	if _, _, err := store.Create(id, realm, declaration); err != nil {
		t.Fatal(err)
	}
	store.TokenKey = nil
	items, err := store.ListOwnedRepositories(t.Context(), realm, []string{declaration.RepoID})
	if err != nil || len(items) != 1 || items[0].ChannelID != id {
		t.Fatalf("read: %v %d", err, len(items))
	}
	if _, err := store.ListOwnedRepositories(t.Context(), uuid.NewString(), []string{declaration.RepoID}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign realm: %v", err)
	}
	if _, err := store.ListOwnedRepositories(t.Context(), realm, []string{uuid.NewString()}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign repo: %v", err)
	}
	items, err = store.ListOwnedRepositories(t.Context(), realm, nil)
	if err != nil || len(items) != 0 {
		t.Fatalf("empty scope: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.ListOwnedRepositories(ctx, realm, []string{declaration.RepoID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	store.Root = filepath.Join(t.TempDir(), "absent")
	items, err = store.ListOwnedRepositories(t.Context(), realm, []string{declaration.RepoID})
	if err != nil || len(items) != 0 {
		t.Fatalf("uninitialized: %v", err)
	}
	if _, err := os.Stat(store.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reader created state directory")
	}
}
