package clientview

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func fixture() View {
	return View{Schema: Schema, ClientID: "e71ecd0b-bd99-489d-b822-41b01bd91346", RealmID: "7b807185-aa75-4169-8a65-705c7cbab176", Generation: 1, GeneratedAt: time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC), ClientRole: "normal", Repositories: []Repository{{RepoID: "5103f16d-7a22-4631-a4f2-765b437201ef", DisplayName: "Dokumenty", URL: "svn+ssh://_filees-client@example.net/repositories/docs", Access: "rw", State: "active"}}, ActiveOperations: []json.RawMessage{}}
}

func TestStoreIfNewerRejectsRollbackAndConflictingGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "view.json")
	first := fixture()
	if changed, err := StoreIfNewer(path, first); err != nil || !changed {
		t.Fatalf("first changed=%v err=%v", changed, err)
	}
	if changed, err := StoreIfNewer(path, first); err != nil || changed {
		t.Fatalf("idempotent changed=%v err=%v", changed, err)
	}
	conflict := first
	conflict.ClientRole = "ro"
	conflict.Repositories[0].Access = "r"
	if _, err := StoreIfNewer(path, conflict); err == nil {
		t.Fatal("conflicting generation accepted")
	}
	newer := conflict
	newer.Generation = 2
	newer.GeneratedAt = newer.GeneratedAt.Add(time.Minute)
	if changed, err := StoreIfNewer(path, newer); err != nil || !changed {
		t.Fatalf("newer changed=%v err=%v", changed, err)
	}
	if _, err := StoreIfNewer(path, first); err == nil {
		t.Fatal("rollback accepted")
	}
}

func TestReadOnlyRoleCannotCarryWritableRepository(t *testing.T) {
	view := fixture()
	view.ClientRole = "ro"
	if err := view.Validate(); err == nil {
		t.Fatal("global ro with rw repository accepted")
	}
}
