package repoworker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRealmAliasesClaimIsImmutableAndGloballyUnique(t *testing.T) {
	root := t.TempDir()
	realms := filepath.Join(root, "admin", "realms")
	if err := os.MkdirAll(realms, 0o700); err != nil {
		t.Fatal(err)
	}
	first, second := uuid.NewString(), uuid.NewString()
	for _, id := range []string{first, second} {
		if err := atomicJSON(filepath.Join(realms, id+".json"), realmRecord{Schema: "filees.realm/v1", RealmID: id, State: "active", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	runner := &publishRunner{}
	store := RealmAliases{ServiceWC: root, Runner: runner}
	if got, err := store.Claim(context.Background(), first, "Anna_2"); err != nil || got != "anna_2" {
		t.Fatalf("first claim = %q, %v", got, err)
	}
	if _, err := store.Claim(context.Background(), second, "anna_2"); !errors.Is(err, ErrAliasUnavailable) {
		t.Fatalf("duplicate claim error = %v", err)
	}
	if _, err := store.Claim(context.Background(), first, "other"); !errors.Is(err, ErrAliasImmutable) {
		t.Fatalf("changed claim error = %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("published %d times, want 1", runner.calls)
	}
}
