package repoworker

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func lockReleaseFixture(t *testing.T) (*FileLockReleaseStore, LockReleaseRequest, *time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	store := &FileLockReleaseStore{Root: t.TempDir(), TTL: 2 * time.Hour, Now: func() time.Time { return now }}
	request := LockReleaseRequest{
		RepoID: uuid.NewString(), Path: "projekty/model.dwg", ObservedLockID: "opaquelocktoken:" + uuid.NewString(),
		RequesterClientID: uuid.NewString(), RequesterRealmID: uuid.NewString(),
		HolderClientID: uuid.NewString(), HolderRealmID: uuid.NewString(),
	}
	return store, request, &now
}

func TestFileLockReleaseStoreRequestIsDurableAndIdempotent(t *testing.T) {
	store, request, _ := lockReleaseFixture(t)
	first, created, err := store.Request(request)
	if err != nil || !created || first.State != LockReleasePending {
		t.Fatalf("first request = %+v created=%v err=%v", first, created, err)
	}
	second, created, err := store.Request(request)
	if err != nil || created || second != first {
		t.Fatalf("retry = %+v created=%v err=%v", second, created, err)
	}
	entries, err := os.ReadDir(store.Root)
	if err != nil || len(entries) != 1 || len(entries[0].Name()) != 64+len(".json") {
		t.Fatalf("durable files = %+v err=%v", entries, err)
	}
	reopened := &FileLockReleaseStore{Root: store.Root, TTL: store.TTL, Now: store.Now}
	loaded, err := reopened.Get(first.RequestID)
	if err != nil || loaded != first {
		t.Fatalf("reopened request = %+v err=%v", loaded, err)
	}
}

func TestFileLockReleaseStoreSeparatesRequestersAndProjectsBothSides(t *testing.T) {
	store, request, _ := lockReleaseFixture(t)
	first, _, err := store.Request(request)
	if err != nil {
		t.Fatal(err)
	}
	other := request
	other.RequesterClientID = uuid.NewString()
	other.RequesterRealmID = uuid.NewString()
	second, created, err := store.Request(other)
	if err != nil || !created || second.RequestID == first.RequestID {
		t.Fatalf("second requester = %+v created=%v err=%v", second, created, err)
	}
	requesterView, err := store.ListForClient(request.RequesterClientID)
	if err != nil || len(requesterView) != 1 || requesterView[0].RequestID != first.RequestID {
		t.Fatalf("requester projection = %+v err=%v", requesterView, err)
	}
	holderView, err := store.ListForClient(request.HolderClientID)
	if err != nil || len(holderView) != 2 {
		t.Fatalf("holder projection = %+v err=%v", holderView, err)
	}
}

func TestFileLockReleaseStoreHolderRespondsExactlyOnce(t *testing.T) {
	store, request, _ := lockReleaseFixture(t)
	record, _, err := store.Request(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Respond(record.RequestID, uuid.NewString(), LockReleaseAccepted); !errors.Is(err, ErrLockReleaseForbidden) {
		t.Fatalf("foreign holder response error = %v", err)
	}
	dismissed, err := store.Respond(record.RequestID, request.HolderClientID, LockReleaseDismissed)
	if err != nil || dismissed.State != LockReleaseDismissed {
		t.Fatalf("dismiss = %+v err=%v", dismissed, err)
	}
	if _, err := store.Respond(record.RequestID, request.HolderClientID, LockReleaseAccepted); !errors.Is(err, ErrLockReleaseTerminal) {
		t.Fatalf("second response error = %v", err)
	}
	if _, err := store.Respond(record.RequestID, request.HolderClientID, LockReleaseLockGone); !errors.Is(err, ErrLockReleaseInvalidState) {
		t.Fatalf("invalid response state error = %v", err)
	}
}

func TestFileLockReleaseStoreExpiresAndAllowsFreshExplicitRequest(t *testing.T) {
	store, request, now := lockReleaseFixture(t)
	first, _, err := store.Request(request)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(store.TTL)
	expired, err := store.Get(first.RequestID)
	if err != nil || expired.State != LockReleaseExpired {
		t.Fatalf("expired = %+v err=%v", expired, err)
	}
	second, created, err := store.Request(request)
	if err != nil || !created || second.State != LockReleasePending || second.RequestID == first.RequestID {
		t.Fatalf("fresh request = %+v created=%v err=%v", second, created, err)
	}
}

func TestFileLockReleaseStoreDoesNotRearmAcceptedLockInstance(t *testing.T) {
	store, request, now := lockReleaseFixture(t)
	first, _, err := store.Request(request)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := store.Respond(first.RequestID, request.HolderClientID, LockReleaseAccepted)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(24 * time.Hour)
	again, created, err := store.Request(request)
	if err != nil || created || again != accepted {
		t.Fatalf("accepted rearm = %+v created=%v err=%v", again, created, err)
	}
}

func TestFileLockReleaseStoreReconcileTracksLockIdentityNotPerson(t *testing.T) {
	store, request, _ := lockReleaseFixture(t)
	record, _, err := store.Request(request)
	if err != nil {
		t.Fatal(err)
	}
	newHolder := LockReleaseObservation{ObservedLockID: request.ObservedLockID, HolderClientID: uuid.NewString(), HolderRealmID: request.HolderRealmID}
	moved, err := store.Reconcile(record.RequestID, &newHolder)
	if err != nil || moved.State != LockReleasePending || moved.HolderClientID != newHolder.HolderClientID {
		t.Fatalf("same-token migration = %+v err=%v", moved, err)
	}
	staleObservation := LockReleaseObservation{ObservedLockID: "opaquelocktoken:" + uuid.NewString(), HolderClientID: uuid.NewString(), HolderRealmID: uuid.NewString()}
	stale, err := store.Reconcile(record.RequestID, &staleObservation)
	if err != nil || stale.State != LockReleaseStale {
		t.Fatalf("replacement lock = %+v err=%v", stale, err)
	}
	if _, err := store.Reconcile(record.RequestID, nil); !errors.Is(err, ErrLockReleaseTerminal) {
		t.Fatalf("terminal reconcile error = %v", err)
	}
}

func TestFileLockReleaseStoreReconcileDistinguishesGoneFromAccepted(t *testing.T) {
	store, request, _ := lockReleaseFixture(t)
	record, _, err := store.Request(request)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := store.Reconcile(record.RequestID, nil)
	if err != nil || gone.State != LockReleaseLockGone {
		t.Fatalf("gone lock = %+v err=%v", gone, err)
	}
}

func TestFileLockReleaseStoreRejectsMalformedLockObservation(t *testing.T) {
	store, request, _ := lockReleaseFixture(t)
	record, _, err := store.Request(request)
	if err != nil {
		t.Fatal(err)
	}
	malformed := LockReleaseObservation{ObservedLockID: "bad\nvalue"}
	if _, err := store.Reconcile(record.RequestID, &malformed); err == nil {
		t.Fatal("malformed lock observation accepted as a stale lock")
	}
	current, err := store.Get(record.RequestID)
	if err != nil || current.State != LockReleasePending {
		t.Fatalf("request changed after malformed observation: %+v err=%v", current, err)
	}
}

func TestFileLockReleaseStoreRejectsUntrustedIdentityAndPath(t *testing.T) {
	store, request, _ := lockReleaseFixture(t)
	for name, mutate := range map[string]func(*LockReleaseRequest){
		"repository": func(r *LockReleaseRequest) { r.RepoID = "../repo" },
		"path":       func(r *LockReleaseRequest) { r.Path = "../secret" },
		"token":      func(r *LockReleaseRequest) { r.ObservedLockID = "bad\nvalue" },
		"requester":  func(r *LockReleaseRequest) { r.RequesterClientID = r.HolderClientID },
	} {
		t.Run(name, func(t *testing.T) {
			bad := request
			mutate(&bad)
			if _, _, err := store.Request(bad); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}
