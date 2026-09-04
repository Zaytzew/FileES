package repoworker

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientview"
	"github.com/google/uuid"
)

func writeLockReleaseProjectionView(t *testing.T, root, clientID, realmID, alias, repoID string) string {
	t.Helper()
	viewPath := filepath.Join(root, "clients", clientID, "view.json")
	view := clientview.View{
		Schema: clientview.Schema, ServerDisplayName: "Serwer testowy", ClientID: clientID, RealmID: realmID, RealmAlias: alias,
		Generation: 1, GeneratedAt: time.Now().UTC(), ClientRole: "normal",
		Repositories: []clientview.Repository{{
			RepoID: repoID, DisplayName: "Projekt", URL: "svn+ssh://_filees-client@example.net/" + repoID,
			Access: "rw", State: "active", OwnerRealmID: realmID,
		}}, ActiveOperations: []json.RawMessage{},
	}
	if _, err := clientview.StoreIfNewer(viewPath, view); err != nil {
		t.Fatal(err)
	}
	return viewPath
}

func lockReleaseProjectionRecord(requesterClient, requesterRealm, holderClient, holderRealm, repoID string, now time.Time) LockReleaseRecord {
	return LockReleaseRecord{
		Schema: lockReleaseRequestSchema, RequestID: uuid.NewString(), RepoID: repoID,
		Path: "projekty/model.dwg", ObservedLockID: "opaquelocktoken:" + uuid.NewString(),
		RequesterClientID: requesterClient, RequesterRealmID: requesterRealm,
		HolderClientID: holderClient, HolderRealmID: holderRealm, State: LockReleasePending,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(3 * time.Hour),
	}
}

func TestServicePublisherProjectsSameLockReleaseRecordToBothSides(t *testing.T) {
	root := t.TempDir()
	repoID := uuid.NewString()
	requesterClient, requesterRealm := uuid.NewString(), uuid.NewString()
	holderClient, holderRealm := uuid.NewString(), uuid.NewString()
	requesterPath := writeLockReleaseProjectionView(t, root, requesterClient, requesterRealm, "projektanci", repoID)
	holderPath := writeLockReleaseProjectionView(t, root, holderClient, holderRealm, "studio", repoID)
	runner := &publishRunner{}
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	publisher := ServicePublisher{ServiceWC: root, Runner: runner, Now: func() time.Time { return now }}
	record := lockReleaseProjectionRecord(requesterClient, requesterRealm, holderClient, holderRealm, repoID, now)
	if err := publisher.PublishLockRelease(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	requester, err := clientview.Load(requesterPath)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := clientview.Load(holderPath)
	if err != nil {
		t.Fatal(err)
	}
	if requester.Generation != 2 || len(requester.LockReleaseRequests) != 1 || requester.LockReleaseRequests[0].Role != "requester" || requester.LockReleaseRequests[0].CounterpartyRealmAlias != "studio" {
		t.Fatalf("requester projection = %+v", requester)
	}
	if holder.Generation != 2 || len(holder.LockReleaseRequests) != 1 || holder.LockReleaseRequests[0].Role != "holder" || holder.LockReleaseRequests[0].CounterpartyRealmAlias != "projektanci" {
		t.Fatalf("holder projection = %+v", holder)
	}
	if err := publisher.PublishLockRelease(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	requester, _ = clientview.Load(requesterPath)
	holder, _ = clientview.Load(holderPath)
	if requester.Generation != 2 || holder.Generation != 2 || runner.calls != 2 {
		t.Fatalf("idempotent generations requester=%d holder=%d publishes=%d", requester.Generation, holder.Generation, runner.calls)
	}
}

func TestServicePublisherMovesHolderProjectionWithSameLockToken(t *testing.T) {
	root := t.TempDir()
	repoID := uuid.NewString()
	requesterClient, requesterRealm := uuid.NewString(), uuid.NewString()
	oldHolder, holderRealm := uuid.NewString(), uuid.NewString()
	newHolder := uuid.NewString()
	requesterPath := writeLockReleaseProjectionView(t, root, requesterClient, requesterRealm, "projektanci", repoID)
	oldPath := writeLockReleaseProjectionView(t, root, oldHolder, holderRealm, "studio", repoID)
	newPath := writeLockReleaseProjectionView(t, root, newHolder, holderRealm, "studio", repoID)
	publisher := ServicePublisher{ServiceWC: root, Runner: &publishRunner{}}
	now := time.Now().UTC()
	record := lockReleaseProjectionRecord(requesterClient, requesterRealm, oldHolder, holderRealm, repoID, now)
	if err := publisher.PublishLockRelease(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	record.HolderClientID = newHolder
	record.UpdatedAt = now.Add(time.Minute)
	if err := publisher.PublishLockRelease(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	requester, _ := clientview.Load(requesterPath)
	previous, _ := clientview.Load(oldPath)
	current, _ := clientview.Load(newPath)
	if len(requester.LockReleaseRequests) != 1 || len(previous.LockReleaseRequests) != 0 || len(current.LockReleaseRequests) != 1 || current.LockReleaseRequests[0].Role != "holder" {
		t.Fatalf("requester=%+v previous=%+v current=%+v", requester.LockReleaseRequests, previous.LockReleaseRequests, current.LockReleaseRequests)
	}
}

func TestServicePublisherRejectsMissingTargetBeforeChangingAnyView(t *testing.T) {
	root := t.TempDir()
	repoID := uuid.NewString()
	requesterClient, requesterRealm := uuid.NewString(), uuid.NewString()
	requesterPath := writeLockReleaseProjectionView(t, root, requesterClient, requesterRealm, "projektanci", repoID)
	record := lockReleaseProjectionRecord(requesterClient, requesterRealm, uuid.NewString(), uuid.NewString(), repoID, time.Now().UTC())
	publisher := ServicePublisher{ServiceWC: root, Runner: &publishRunner{}}
	if err := publisher.PublishLockRelease(context.Background(), record); err == nil {
		t.Fatal("missing holder projection accepted")
	}
	requester, err := clientview.Load(requesterPath)
	if err != nil || requester.Generation != 1 || len(requester.LockReleaseRequests) != 0 {
		t.Fatalf("requester changed before target validation: %+v err=%v", requester, err)
	}
}
