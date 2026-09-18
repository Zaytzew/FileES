//go:build !windows

package activation

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientview"

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

// A demo server tells the client when its realm ends, in the same commit as
// the first view, and counted from the very activated_at the demo reaper uses.
// It says so in a file of its own: view.json is read strictly, and a field
// there would make the projection unreadable to every client that predates it.
func TestDemoActivationAnnouncesWhenTheRealmEnds(t *testing.T) {
	manager, config := newActivationTestManager(t)
	manager.config.SingleActivationPerRealm = true
	manager.config.DemoRealmTTL = 120 * time.Minute

	grant := testActivationGrant(t, time.Now().Add(time.Hour))
	if err := manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	if err := manager.RecordProof(grant.OperationID, grant.ClientID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	activatedAt, found, err := manager.RealmActivatedAt(grant.RealmID)
	if err != nil || !found {
		t.Fatalf("activatedAt found=%v err=%v", found, err)
	}

	clientDir := filepath.Join(config.ServiceWorkingCopy, "clients", grant.ClientID)
	demo, found, err := clientview.LoadDemo(filepath.Join(clientDir, clientview.DemoFileName))
	if err != nil || !found {
		t.Fatalf("demo announcement found=%v err=%v", found, err)
	}
	if want := activatedAt.Add(120 * time.Minute).UTC(); !demo.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at=%s, want activated_at+TTL=%s", demo.ExpiresAt, want)
	}
	// Committed, not only written: the client receives it by svn update.
	status, err := exec.Command("svn", "status", "--non-interactive", filepath.Join(clientDir, clientview.DemoFileName)).CombinedOutput()
	if err != nil || strings.TrimSpace(string(status)) != "" {
		t.Fatalf("demo.json not committed: %q err=%v", status, err)
	}
	// And view.json stays exactly what every client already reads.
	if _, err := clientview.Load(filepath.Join(clientDir, "view.json")); err != nil {
		t.Fatalf("view.json no longer decodes strictly: %v", err)
	}
}

func TestOrdinaryServerAnnouncesNoDemo(t *testing.T) {
	manager, config := newActivationTestManager(t)
	grant := testActivationGrant(t, time.Now().Add(time.Hour))
	if err := manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	if err := manager.RecordProof(grant.OperationID, grant.ClientID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	if _, found, err := clientview.LoadDemo(filepath.Join(config.ServiceWorkingCopy, "clients", grant.ClientID, clientview.DemoFileName)); err != nil || found {
		t.Fatalf("ordinary server announced a demo: found=%v err=%v", found, err)
	}
}
