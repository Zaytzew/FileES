//go:build !windows

package servertool

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/activation"
	control "filees/pkg/control/v1"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

// TestDemoReapDeletesExpiredRealmsToZero runs the demo expiry on the same real
// SVN fixture as the destructive realm-removal E2E: a realm past its TTL loses
// every owned repository without an archive and every credential, while a
// realm still inside its TTL is untouched. A second reap does nothing.
func TestDemoReapDeletesExpiredRealmsToZero(t *testing.T) {
	f := newRealmRemovalE2EFixture(t, 0)
	time.Sleep(20 * time.Millisecond)
	youngRealm := uuid.NewString()
	youngClient := f.activate(t, youngRealm)
	youngRepo := f.createRepository(t, youngRealm, "young")

	manager, err := activation.NewUnderServiceWorkingCopyLock(f.activationConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	activations, err := manager.RealmActivations()
	if err != nil {
		t.Fatal(err)
	}
	policy := serverconfig.DemoPolicy{Enabled: true, RealmTTL: time.Minute}
	youngAt := activations[youngRealm]
	if !activations[f.targetRealm].Before(youngAt) || !activations[f.otherRealm].Before(youngAt) {
		t.Fatalf("fixture activations are not ordered: %v", activations)
	}
	// The instant the young realm still has one nanosecond left.
	now := youngAt.Add(policy.RealmTTL - time.Nanosecond)

	admission := demoRealmAdmission{Policy: policy, Activations: manager, Now: func() time.Time { return now }}
	if err := admission.Admit(repoworker.Session{RealmID: f.targetRealm}, control.Ticket{}); !errors.Is(err, errDemoRealmExpired) {
		t.Fatalf("expired realm admitted: %v", err)
	}
	if err := admission.Admit(repoworker.Session{RealmID: youngRealm}, control.Ticket{}); err != nil {
		t.Fatalf("live realm refused: %v", err)
	}
	if err := demoClientExpired(policy, manager, f.targetClients[0].OperationID, f.targetClients[0].ClientID, now); !errors.Is(err, errDemoRealmExpired) {
		t.Fatalf("expired client entry admitted: %v", err)
	}
	if err := demoClientExpired(policy, manager, youngClient.OperationID, youngClient.ClientID, now); err != nil {
		t.Fatalf("live client entry refused: %v", err)
	}

	store := repoworker.RealmRemovalStore{
		Root: filepath.Join(f.resultsRoot, "realm-removals"), OTPPepper: bytes.Repeat([]byte{0x5a}, 32),
		TTL: time.Hour, Attempts: 3,
	}
	recovery := realmRecoveryPublisher{
		ArchiveRoot: f.archiveRoot,
		Manifests:   repoworker.RecoveryManifestStore{Root: filepath.Join(f.resultsRoot, "recovery-manifests")},
		Keys:        repoworker.RecoveryKeyStore{Root: filepath.Join(f.resultsRoot, "recovery-keys")},
	}
	runtime := realmRemovalRuntime{
		store: store, manager: manager, publisher: f.publisher,
		executor: realmRemovalExecutor{
			Store: store, Backend: f.backend, Recovery: recovery, Publisher: f.publisher, Activation: manager,
			Erasure: repoworker.DataErasureStore{Root: filepath.Join(f.resultsRoot, "data-erasure")}, ErasureMaxDays: 90,
		},
	}
	result, err := reapExpiredDemoRealms(context.Background(), runtime, policy, f.activationConfig, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 2 || !containsString(result.Removed, f.targetRealm) || !containsString(result.Removed, f.otherRealm) || containsString(result.Expired, youngRealm) {
		t.Fatalf("reap result=%+v, want target and other removed, young kept", result)
	}

	for _, realm := range []string{f.targetRealm, f.otherRealm} {
		if clients, err := manager.ActiveClientsInRealm(realm); err != nil || len(clients) != 0 {
			t.Fatalf("expired realm %s keeps clients=%v err=%v", realm, clients, err)
		}
	}
	for _, repository := range append(append([]repoworker.Repository{}, f.ownedRepos...), f.foreignRepos...) {
		if _, err := os.Lstat(filepath.Join(f.repositoriesRoot, repository.RepoID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expired realm FSFS survived: %s err=%v", repository.RepoID, err)
		}
	}
	if entries, err := os.ReadDir(f.archiveRoot); err == nil {
		for _, entry := range entries {
			t.Fatalf("demo expiry left an archive: %s", entry.Name())
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	keys, err := os.ReadFile(f.activationConfig.AuthorizedKeysFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(keys, []byte(f.targetClients[0].ClientID)) || bytes.Contains(keys, []byte(f.otherClient.ClientID)) {
		t.Fatal("expired realm credential is still authorized")
	}
	if !bytes.Contains(keys, []byte(youngClient.ClientID)) {
		t.Fatal("live realm credential was revoked")
	}
	if _, err := os.Stat(filepath.Join(f.repositoriesRoot, youngRepo.RepoID, "format")); err != nil {
		t.Fatalf("live realm repository removed: %v", err)
	}

	again, err := reapExpiredDemoRealms(context.Background(), runtime, policy, f.activationConfig, now)
	if err != nil || len(again.Removed) != 0 || len(again.Expired) != 2 {
		t.Fatalf("second reap=%+v err=%v, want nothing removed", again, err)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
