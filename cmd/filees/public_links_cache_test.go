package main

import (
	"context"
	"errors"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/publicstate"
	"github.com/google/uuid"
)

func TestPublicLinksOwnACKWinsOverOldReadWithoutRefreshingSiblings(t *testing.T) {
	c := newPublicShareCache()
	b := publicstate.Binding{ServerID: "lab", ClientID: uuid.NewString(), RealmID: uuid.NewString()}
	c.Bind(b.ServerID, b.ClientID, "host", 22, "")
	now := time.Now().UTC()
	c.now = func() time.Time { return now }
	item := publicstate.Item{RepoDisplayName: "Project", Share: control.PublicShareSummary{ChannelID: uuid.NewString(), RepoID: uuid.NewString(), State: "active"}}
	sibling := item
	sibling.Share.ChannelID = uuid.NewString()
	epoch := c.BeginRefresh("lab")
	req, serial := c.stateRequest("lab", b.RealmID, epoch)
	initial, _ := publicstate.Build(b, req, []publicstate.Item{item, sibling})
	if err := c.completeState("lab", epoch, serial, req, initial, b); err != nil {
		t.Fatal(err)
	}
	c.EndRefresh("lab", epoch)
	oldObserved := c.Snapshot().AsOf
	oldReq, oldSerial := c.stateRequest("lab", b.RealmID, epoch)
	oldResponse, _ := publicstate.Build(b, oldReq, []publicstate.Item{item, sibling})
	now = now.Add(time.Minute)
	mutationEpoch, release, err := c.beginMutation(t.Context(), "lab")
	if err != nil {
		t.Fatal(err)
	}
	duringReq, duringSerial := c.stateRequest("lab", b.RealmID, epoch)
	duringResponse, _ := publicstate.Build(b, duringReq, []publicstate.Item{item, sibling})
	if err := c.completeState("lab", epoch, duringSerial, duringReq, duringResponse, b); !errors.Is(err, errPublicLinksSuperseded) {
		t.Fatalf("read during mutation: %v", err)
	}
	item.Share.State = "revoked"
	ack := control.PublicShareResult{ChannelID: item.Share.ChannelID, State: "revoked", StateReceipt: &control.PublicShareStateReceipt{RealmID: b.RealmID, RepoID: item.Share.RepoID, RepoDisplayName: item.RepoDisplayName, Share: &item.Share}}
	if !c.applyReceipt("lab", b.ClientID, mutationEpoch, ack) {
		t.Fatal("ACK rejected")
	}
	release()
	if err := c.completeState("lab", epoch, oldSerial, oldReq, oldResponse, b); !errors.Is(err, errPublicLinksSuperseded) {
		t.Fatalf("old read overwrote ACK: %v", err)
	}
	if err := c.completeState("lab", epoch, duringSerial, duringReq, duringResponse, b); !errors.Is(err, errPublicLinksSuperseded) {
		t.Fatalf("read begun during mutation overwrote ACK: %v", err)
	}
	got := c.Snapshot()
	if got.AsOf != oldObserved || len(got.Shares) != 2 {
		t.Fatalf("ACK refreshed collection: %+v", got)
	}
	for _, row := range got.Shares {
		if row.ChannelID == sibling.Share.ChannelID && row.ObservedAt != oldObserved {
			t.Fatal("sibling refreshed by unrelated ACK")
		}
		if row.ChannelID == item.Share.ChannelID && (row.State != "revoked" || row.ObservedAt != now.Format(time.RFC3339Nano)) {
			t.Fatal("missing canonical item")
		}
	}
	req, serial = c.stateRequest("lab", b.RealmID, epoch)
	unchanged, _ := publicstate.Build(b, req, []publicstate.Item{item, sibling})
	if len(unchanged.Changes) != 0 {
		t.Fatal("own ACK was not included in next delta baseline")
	}
	if err := c.completeState("lab", epoch, serial, req, unchanged, b); err != nil {
		t.Fatal(err)
	}
}

func TestPublicLinksReceiptBeforeInitDeletionLostACKAndActivationFence(t *testing.T) {
	c := newPublicShareCache()
	clientID, realmID, repoID, channelID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	c.Bind("lab", clientID, "host", 22, "")
	epoch, release, _ := c.beginMutation(t.Context(), "lab")
	row := control.PublicShareSummary{ChannelID: channelID, RepoID: repoID, State: "active"}
	ack := control.PublicShareResult{ChannelID: channelID, State: "active", StateReceipt: &control.PublicShareStateReceipt{RealmID: realmID, RepoID: repoID, Share: &row}}
	if !c.applyReceipt("lab", clientID, epoch, ack) {
		t.Fatal("create ACK rejected before init")
	}
	release()
	if got := c.Snapshot(); !got.Stale || got.AsOf != "" || len(got.Shares) != 1 {
		t.Fatalf("single ACK claimed complete inventory: %+v", got)
	}
	_, release, _ = c.beginMutation(t.Context(), "lab")
	// Lost response: no receipt, do not guess deletion or creation.
	release()
	if len(c.List()) != 1 {
		t.Fatal("lost ACK modified inventory")
	}
	epoch, release, _ = c.beginMutation(t.Context(), "lab")
	ack.State, ack.StateReceipt.Share = "deleted", nil
	if !c.applyReceipt("lab", clientID, epoch, ack) {
		t.Fatal("delete receipt rejected")
	}
	release()
	if len(c.List()) != 0 {
		t.Fatal("deleted item retained")
	}
	epoch, release, _ = c.beginMutation(t.Context(), "lab")
	c.Bind("lab", uuid.NewString(), "host", 22, "")
	if c.applyReceipt("lab", clientID, epoch, ack) {
		t.Fatal("old activation ACK accepted")
	}
	release()
}

func TestPublicLinksOwnMutationQueueCancellation(t *testing.T) {
	c := newPublicShareCache()
	_, release, err := c.beginMutation(t.Context(), "lab")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := c.beginMutation(ctx, "lab"); !errors.Is(err, context.Canceled) {
		t.Fatalf("queue ignored cancellation: %v", err)
	}
	// A different server has an independent gate.
	_, otherRelease, err := c.beginMutation(t.Context(), "other")
	if err != nil {
		t.Fatal(err)
	}
	otherRelease()
	release()
	_, release, err = c.beginMutation(t.Context(), "lab")
	if err != nil {
		t.Fatal(err)
	}
	release()
}
