package repoworker

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"filees/pkg/clientview"
)

type maintenanceRunner struct {
	calls int
	err   error
}

func (r *maintenanceRunner) Publish(context.Context, []string, string) error { r.calls++; return r.err }

func TestLockReleaseSweepRetainsConsentUntilTokenGoneAndCommitSucceeds(t *testing.T) {
	store, input, now := lockReleaseFixture(t)
	record, _, err := store.Request(input)
	if err != nil {
		t.Fatal(err)
	}
	record, err = store.Respond(record.RequestID, input.HolderClientID, LockReleaseAccepted)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	requester := writeLockReleaseProjectionView(t, root, input.RequesterClientID, input.RequesterRealmID, "requester", input.RepoID)
	holder := writeLockReleaseProjectionView(t, root, input.HolderClientID, input.HolderRealmID, "holder", input.RepoID)
	runner := &maintenanceRunner{}
	publisher := ServicePublisher{ServiceWC: root, Runner: runner}
	authority := &fakeLockReleaseAuthority{observation: &LockReleaseObservation{ObservedLockID: input.ObservedLockID, HolderClientID: input.HolderClientID, HolderRealmID: input.HolderRealmID}}
	*now = now.Add(30 * 24 * time.Hour)
	if n, err := store.Sweep(t.Context(), authority, publisher); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	if _, fresh, err := store.Request(input); err != nil || fresh {
		t.Fatal("rearmed live consent", err)
	}
	authority.err = errors.New("unavailable")
	if n, err := store.Sweep(t.Context(), authority, publisher); n != 0 || err == nil {
		t.Fatal(n, err)
	}
	authority.err, authority.observation = nil, nil
	runner.err = errors.New("commit lost reply")
	if n, err := store.Sweep(t.Context(), authority, publisher); n != 0 || err == nil {
		t.Fatal(n, err)
	}
	if _, err := store.Get(record.RequestID); err != nil {
		t.Fatal("removed before commit", err)
	}
	before := runner.calls
	runner.err = nil
	restarted := &FileLockReleaseStore{Root: store.Root, Now: store.Now}
	if n, err := restarted.Sweep(t.Context(), authority, publisher); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	if runner.calls <= before {
		t.Fatal("retry did not publish locally removed views")
	}
	if _, err := restarted.Get(record.RequestID); !errors.Is(err, ErrLockReleaseNotFound) {
		t.Fatal(err)
	}
	for _, path := range []string{requester, holder} {
		view, err := clientview.Load(path)
		if err != nil || len(view.LockReleaseRequests) != 0 {
			t.Fatal(view, err)
		}
	}
}

func TestLockReleaseSweepExpiresProjectsAndRefusesCorruptView(t *testing.T) {
	store, input, now := lockReleaseFixture(t)
	record, _, _ := store.Request(input)
	root := t.TempDir()
	requester := writeLockReleaseProjectionView(t, root, input.RequesterClientID, input.RequesterRealmID, "requester", input.RepoID)
	writeLockReleaseProjectionView(t, root, input.HolderClientID, input.HolderRealmID, "holder", input.RepoID)
	publisher := ServicePublisher{ServiceWC: root, Runner: &maintenanceRunner{}}
	authority := &fakeLockReleaseAuthority{}
	*now = now.Add(4 * time.Hour)
	if n, err := store.Sweep(t.Context(), authority, publisher); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	view, err := clientview.Load(requester)
	if err != nil || len(view.LockReleaseRequests) != 1 || view.LockReleaseRequests[0].State != "expired" {
		t.Fatal(view, err)
	}
	*now = now.Add(8 * 24 * time.Hour)
	if err := os.WriteFile(requester, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := store.Sweep(t.Context(), authority, publisher); n != 0 || err == nil {
		t.Fatal(n, err)
	}
	if _, err := store.Get(record.RequestID); err != nil {
		t.Fatal(err)
	}
}
