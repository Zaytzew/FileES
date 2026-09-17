//go:build !windows

package activation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDemoRealmAdmitsOneActivationEver(t *testing.T) {
	manager, _ := newActivationTestManager(t)
	manager.config.SingleActivationPerRealm = true
	realmID := uuid.NewString()

	first := testActivationGrant(t, time.Now().Add(time.Hour))
	first.RealmID = realmID
	if err := manager.Stage(first); err != nil {
		t.Fatal(err)
	}
	// Retrying the same operation is not a second activation.
	if err := manager.Stage(first); err != nil {
		t.Fatalf("idempotent restage refused: %v", err)
	}
	if err := manager.RecordProof(first.OperationID, first.ClientID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	second := testActivationGrant(t, time.Now().Add(time.Hour))
	second.RealmID = realmID
	if err := manager.Stage(second); !errors.Is(err, ErrRealmAlreadyActivated) {
		t.Fatalf("second activation err=%v, want ErrRealmAlreadyActivated", err)
	}

	if _, err := manager.RevokeRealm(context.Background(), realmID, "demo expired"); err != nil {
		t.Fatal(err)
	}
	third := testActivationGrant(t, time.Now().Add(time.Hour))
	third.RealmID = realmID
	if err := manager.Stage(third); !errors.Is(err, ErrRealmAlreadyActivated) {
		t.Fatalf("activation after revoke err=%v, want ErrRealmAlreadyActivated", err)
	}

	other := testActivationGrant(t, time.Now().Add(time.Hour))
	if err := manager.Stage(other); err != nil {
		t.Fatalf("another realm refused: %v", err)
	}

	activatedAt, found, err := manager.RealmActivatedAt(realmID)
	if err != nil || !found || activatedAt.IsZero() {
		t.Fatalf("activatedAt=%v found=%v err=%v", activatedAt, found, err)
	}
	if _, found, err := manager.RealmActivatedAt(other.RealmID); err != nil || found {
		t.Fatalf("staged-only realm reported as activated: found=%v err=%v", found, err)
	}
}

func TestOrdinaryServerStillJoinsRealms(t *testing.T) {
	manager, _ := newActivationTestManager(t)
	realmID := uuid.NewString()
	for range 2 {
		grant := testActivationGrant(t, time.Now().Add(time.Hour))
		grant.RealmID = realmID
		if err := manager.Stage(grant); err != nil {
			t.Fatalf("ordinary server refused a joining installation: %v", err)
		}
	}
}
