package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"filees/pkg/clientview"
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

func TestRealmAliasesClaimSkipsForeignClientViews(t *testing.T) {
	root := t.TempDir()
	realms := filepath.Join(root, "admin", "realms")
	if err := os.MkdirAll(realms, 0o700); err != nil {
		t.Fatal(err)
	}
	// Directory iteration is lexical on the supported filesystems; use fixed
	// valid UUIDs so a foreign view is visited before the claimant's view.
	foreignRealm := "00000000-0000-4000-8000-000000000001"
	ownedRealm := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	for _, id := range []string{foreignRealm, ownedRealm} {
		if err := atomicJSON(filepath.Join(realms, id+".json"), realmRecord{Schema: "filees.realm/v1", RealmID: id, State: "active", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	writeView := func(clientID, realmID string) {
		view := clientview.View{Schema: clientview.Schema, ServerDisplayName: "Serwer testowy", ClientID: clientID, RealmID: realmID, Generation: 1, GeneratedAt: time.Now(), ClientRole: "normal", Capabilities: &clientview.Capabilities{CanCreateRepositories: true}, Repositories: []clientview.Repository{}, ActiveOperations: []json.RawMessage{}}
		if _, err := clientview.StoreIfNewer(filepath.Join(root, "clients", clientID, "view.json"), view); err != nil {
			t.Fatal(err)
		}
	}
	writeView("00000000-0000-4000-8000-000000000001", foreignRealm)
	ownedClient := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	writeView(ownedClient, ownedRealm)
	runner := &publishRunner{}
	store := RealmAliases{ServiceWC: root, Runner: runner}
	if got, err := store.Claim(context.Background(), ownedRealm, "Biuro"); err != nil || got != "biuro" {
		t.Fatalf("claim = %q, %v", got, err)
	}
	if runner.calls != 1 {
		t.Fatalf("published %d times, want 1", runner.calls)
	}
	view, err := clientview.Load(filepath.Join(root, "clients", ownedClient, "view.json"))
	if err != nil || view.RealmAlias != "biuro" {
		t.Fatalf("owned view = %+v, %v", view, err)
	}
	// A matching retry must repair a stale projection, not return before
	// visiting client views merely because the canonical realm already has the
	// requested immutable alias.
	view.RealmAlias = ""
	view.Generation++
	view.GeneratedAt = view.GeneratedAt.Add(time.Second)
	if _, err := clientview.StoreIfNewer(filepath.Join(root, "clients", ownedClient, "view.json"), view); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Claim(context.Background(), ownedRealm, "biuro"); err != nil || got != "biuro" {
		t.Fatalf("idempotent repair claim = %q, %v", got, err)
	}
	view, err = clientview.Load(filepath.Join(root, "clients", ownedClient, "view.json"))
	if err != nil || view.RealmAlias != "biuro" || runner.calls != 2 {
		t.Fatalf("repaired view=%+v err=%v publishes=%d", view, err, runner.calls)
	}
}
