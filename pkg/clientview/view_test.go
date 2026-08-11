package clientview

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func fixture() View {
	return View{Schema: Schema, ClientID: "e71ecd0b-bd99-489d-b822-41b01bd91346", RealmID: "7b807185-aa75-4169-8a65-705c7cbab176", Generation: 1, GeneratedAt: time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC), ClientRole: "normal", Repositories: []Repository{{RepoID: "5103f16d-7a22-4631-a4f2-765b437201ef", DisplayName: "Dokumenty", URL: "svn+ssh://_filees-client@example.net/repositories/docs", Access: "rw", State: "active"}}, ActiveOperations: []json.RawMessage{}}
}

func TestViewAcceptsSeparatedDataRepositoryAccount(t *testing.T) {
	view := fixture()
	view.Repositories[0].URL = "svn+ssh://_filees-data@example.net/" + view.Repositories[0].RepoID
	if err := view.Validate(); err != nil {
		t.Fatal(err)
	}
	view.Repositories[0].URL = "svn+ssh://root@example.net/repo"
	if err := view.Validate(); err == nil {
		t.Fatal("arbitrary SSH account accepted")
	}
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

func TestRepositoryPolicyAndCreationCapabilities(t *testing.T) {
	view := fixture()
	if !view.CanCreateRepositories() {
		t.Fatal("legacy normal projection lost repository creation capability")
	}
	view.Capabilities = &Capabilities{CanCreateRepositories: false}
	view.Repositories[0].OwnerRealmID = view.RealmID
	view.Repositories[0].AttachmentPolicy = "required"
	if err := view.Validate(); err != nil {
		t.Fatal(err)
	}
	if view.CanCreateRepositories() {
		t.Fatal("explicit managed profile can create repositories")
	}
	view.ClientRole = "ro"
	view.Repositories[0].Access = "r"
	view.Capabilities.CanCreateRepositories = true
	if err := view.Validate(); err == nil {
		t.Fatal("read-only profile with creation capability accepted")
	}
}

func TestRepositoryPolicyValidationFailsClosed(t *testing.T) {
	view := fixture()
	view.Repositories[0].OwnerRealmID = "not-a-uuid"
	if err := view.Validate(); err == nil {
		t.Fatal("invalid owner realm accepted")
	}
	view.Repositories[0].OwnerRealmID = view.RealmID
	view.Repositories[0].AttachmentPolicy = "automatic"
	if err := view.Validate(); err == nil {
		t.Fatal("unknown attachment policy accepted")
	}
}

func TestEditingPolicyValidationFailsClosed(t *testing.T) {
	view := fixture()
	if err := view.Validate(); err != nil {
		t.Fatalf("default (absent) policy rejected: %v", err)
	}
	view.Repositories[0].EditingPolicy = EditingLockRequired
	if err := view.Validate(); err != nil {
		t.Fatalf("lock_required rejected: %v", err)
	}
	// "free" is an input alias only. Reaching Validate means a writer failed
	// to normalise it, which would put the default on the wire and break
	// every older reader for no benefit at all.
	view.Repositories[0].EditingPolicy = "free"
	if err := view.Validate(); err == nil {
		t.Fatal("unnormalised \"free\" accepted into a projection")
	}
	view.Repositories[0].EditingPolicy = "readonly"
	if err := view.Validate(); err == nil {
		t.Fatal("unknown editing policy accepted")
	}
}

// The whole rollout rests on this: the default must never be serialised.
// Decode rejects unknown fields, so any projection that carries the key is
// unreadable to a binary that predates it - and that failure takes the entire
// view, not one repository. As long as free repositories omit the key, older
// readers keep working and the change stays inert until an owner opts in.
func TestDefaultEditingPolicyNeverReachesTheWire(t *testing.T) {
	view := fixture()
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("editing_policy")) {
		t.Fatalf("default policy serialised, which breaks older readers: %s", raw)
	}
	if got := NormalizeEditingPolicy("free"); got != EditingFree {
		t.Fatalf("NormalizeEditingPolicy(\"free\")=%q, want the empty default", got)
	}

	view.Repositories[0].EditingPolicy = EditingLockRequired
	raw, err = json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"editing_policy":"lock_required"`)) {
		t.Fatalf("opted-in policy missing from the projection: %s", raw)
	}

	// Round-trips through the strict decoder that guards every reader.
	decoded, err := Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("strict decode of our own projection failed: %v", err)
	}
	if !decoded.Repositories[0].RequiresLock() {
		t.Fatal("policy lost in round-trip")
	}
}
