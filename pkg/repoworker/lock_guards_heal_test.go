package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientview"
	control "filees/pkg/control/v1"
	"github.com/google/uuid"
)

func TestLockGuardsMissingOnlyWithoutForeignHooks(t *testing.T) {
	for _, tc := range []struct {
		states []LockGuardStatus
		want   bool
	}{
		{[]LockGuardStatus{{"pre-lock", "missing"}, {"pre-unlock", "missing"}}, true},
		{[]LockGuardStatus{{"pre-lock", "installed"}, {"pre-unlock", "missing"}}, true},
		{[]LockGuardStatus{{"pre-lock", "installed"}, {"pre-unlock", "installed"}}, false},
		{[]LockGuardStatus{{"pre-lock", "foreign"}, {"pre-unlock", "missing"}}, false},
	} {
		if got := lockGuardsMissing(tc.states); got != tc.want {
			t.Fatalf("%+v: got %v, want %v", tc.states, got, tc.want)
		}
	}
}

// A repository created before r914 has no guards. Owner's production,
// 2026-09-25: every passport of such a repository was refused as
// "unavailable" and the client retried forever. Arming now installs them.
func TestPassportArmInstallsMissingGuardsButNeverAForeignHook(t *testing.T) {
	f := newExecutionFixture(t)
	hooks := filepath.Join(f.repo, "hooks")
	for _, hook := range []string{"pre-lock", "pre-unlock"} {
		if err := os.Remove(filepath.Join(hooks, hook)); err != nil {
			t.Fatal(err)
		}
	}
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
	states, err := InspectLockGuards(f.repo, os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if state.State != "installed" {
			t.Fatalf("guard not installed by arming: %+v", states)
		}
	}

	g := newExecutionFixture(t)
	foreign := filepath.Join(g.repo, "hooks", "pre-unlock")
	if err := os.Remove(filepath.Join(g.repo, "hooks", "pre-lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(foreign); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	g.handle(t, control.TicketArmPassportAcquisition, control.ResultError)
	if raw, err := os.ReadFile(foreign); err != nil || string(raw) != "#!/bin/sh\nexit 0\n" {
		t.Fatalf("operator's hook was touched: %q %v", raw, err)
	}
	if _, err := os.Lstat(filepath.Join(g.repo, "hooks", "pre-lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a guard was installed next to a foreign hook: %v", err)
	}
}

// Switching a repository to borrowing installs its guards first, also when the
// policy is already set (the owner's way to repair an older repository), and
// a failure leaves the policy unchanged.
func TestLockRequiredPolicyEnsuresGuardsFirst(t *testing.T) {
	root := t.TempDir()
	runner := &publishRunner{}
	owner, repoID := uuid.NewString(), uuid.NewString()
	recordPath := filepath.Join(root, "admin", "repositories", repoID+".json")
	if err := atomicJSON(filepath.Join(root, "admin", "realms", owner+".json"), realmRecord{Schema: "filees.realm/v1", RealmID: owner, State: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(recordPath, repositoryRecord{Schema: RepositorySchema, RepoID: repoID, OwnerRealmID: owner, DisplayName: "Rysunki", URL: "svn+ssh://_filees-data@example/" + repoID, State: "active"}); err != nil {
		t.Fatal(err)
	}
	client := uuid.NewString()
	if err := atomicJSON(filepath.Join(root, "admin", "clients", client+".json"), map[string]any{"schema": "filees.client-instance/v1", "client_id": client, "realm_id": owner, "state": "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := clientview.StoreIfNewer(filepath.Join(root, "clients", client, "view.json"), clientview.View{Schema: clientview.Schema, ServerDisplayName: "Serwer testowy", ClientID: client, RealmID: owner, Generation: 1, GeneratedAt: time.Now().UTC(), ClientRole: "normal", Capabilities: &clientview.Capabilities{CanCreateRepositories: true}, Repositories: []clientview.Repository{}, ActiveOperations: []json.RawMessage{}}); err != nil {
		t.Fatal(err)
	}
	var ensured []string
	failure := errors.New("preserving existing pre-lock: explicit hook integration required")
	var ensureErr error
	p := ServicePublisher{ServiceWC: root, DataAuthzFile: filepath.Join(root, "authz"), Runner: runner, EnsureLockGuards: func(id string) error {
		ensured = append(ensured, id)
		return ensureErr
	}}
	ctx := context.Background()

	ensureErr = failure
	if _, err := p.SetRepositoryEditingPolicy(ctx, owner, repoID, "lock_required"); !errors.Is(err, failure) {
		t.Fatalf("policy accepted without guards: %v", err)
	}
	if got := loadRepositoryRecordForTest(t, recordPath).EditingPolicy; got != clientview.EditingFree {
		t.Fatalf("a refused policy was stored: %q", got)
	}

	ensureErr = nil
	for i := 0; i < 2; i++ {
		if got, err := p.SetRepositoryEditingPolicy(ctx, owner, repoID, "lock_required"); err != nil || got != clientview.EditingLockRequired {
			t.Fatalf("pass %d: got=%q err=%v", i, got, err)
		}
	}
	if _, err := p.SetRepositoryEditingPolicy(ctx, owner, repoID, "free"); err != nil {
		t.Fatal(err)
	}
	if len(ensured) != 3 || ensured[2] != repoID {
		t.Fatalf("guards ensured %v, want once per lock_required request and never for free", ensured)
	}
}
