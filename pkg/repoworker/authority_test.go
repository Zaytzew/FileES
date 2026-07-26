package repoworker

import (
	"context"
	"encoding/json"
	"filees/pkg/clientview"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type publishRunner struct{ calls int }

func (r *publishRunner) Publish(context.Context, []string, string) error { r.calls++; return nil }
func TestServicePublisherProjectsOnlyOwnerRealmAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	realm, other := uuid.NewString(), uuid.NewString()
	ownerClient, otherClient := uuid.NewString(), uuid.NewString()
	writeView := func(client, realm string) {
		v := clientview.View{Schema: clientview.Schema, ClientID: client, RealmID: realm, Generation: 1, GeneratedAt: time.Now(), ClientRole: "normal", Capabilities: &clientview.Capabilities{CanCreateRepositories: true}, Repositories: []clientview.Repository{}, ActiveOperations: []json.RawMessage{}}
		if _, e := clientview.StoreIfNewer(filepath.Join(root, "clients", client, "view.json"), v); e != nil {
			t.Fatal(e)
		}
	}
	writeView(ownerClient, realm)
	writeView(otherClient, other)
	run := &publishRunner{}
	authz := filepath.Join(t.TempDir(), "data.authz")
	p := ServicePublisher{ServiceWC: root, DataAuthzFile: authz, Runner: run}
	repo := uuid.NewString()
	url := "svn+ssh://_filees-client@example/repos/" + repo
	if e := p.Publish(context.Background(), repo, realm, "Docs", url); e != nil {
		t.Fatal(e)
	}
	if e := p.Publish(context.Background(), repo, realm, "Docs", url); e != nil {
		t.Fatal(e)
	}
	owner, _ := clientview.Load(filepath.Join(root, "clients", ownerClient, "view.json"))
	foreign, _ := clientview.Load(filepath.Join(root, "clients", otherClient, "view.json"))
	if owner.Generation != 2 || len(owner.Repositories) != 1 || foreign.Generation != 1 || len(foreign.Repositories) != 0 {
		t.Fatalf("owner=%+v foreign=%+v", owner, foreign)
	}
	if owner.Repositories[0].State != "initializing" {
		t.Fatalf("new repository state = %q", owner.Repositories[0].State)
	}
	if err := p.Activate(context.Background(), repo, other); err == nil {
		t.Fatal("foreign realm activated repository")
	}
	if err := p.Activate(context.Background(), repo, realm); err != nil {
		t.Fatal(err)
	}
	if err := p.Activate(context.Background(), repo, realm); err != nil {
		t.Fatalf("idempotent activation failed: %v", err)
	}
	owner, _ = clientview.Load(filepath.Join(root, "clients", ownerClient, "view.json"))
	if owner.Generation != 3 || owner.Repositories[0].State != "active" {
		t.Fatalf("activated owner projection = %+v", owner)
	}
	raw, _ := os.ReadFile(authz)
	if !strings.Contains(string(raw), ownerClient) || strings.Contains(string(raw), otherClient) {
		t.Fatalf("authz=%s", raw)
	}
}

// TestTransferOwnerMovesRepositoryAndRegeneratesAuthz is the admin
// repo-ownership-transfer regression guard
// (AUTOLOCK_CREATOR_OWNERSHIP_CONCEPT_V2.md §6): the old owner's client
// projection loses the repository entirely, the new owner's client gains rw
// on it, and authz's owner group flips from old to new.
func TestTransferOwnerMovesRepositoryAndRegeneratesAuthz(t *testing.T) {
	root := t.TempDir()
	oldRealm, newRealm := uuid.NewString(), uuid.NewString()
	oldClient, newClient := uuid.NewString(), uuid.NewString()
	writeView := func(client, realm string) {
		v := clientview.View{Schema: clientview.Schema, ClientID: client, RealmID: realm, Generation: 1, GeneratedAt: time.Now(), ClientRole: "normal", Capabilities: &clientview.Capabilities{CanCreateRepositories: true}, Repositories: []clientview.Repository{}, ActiveOperations: []json.RawMessage{}}
		if _, e := clientview.StoreIfNewer(filepath.Join(root, "clients", client, "view.json"), v); e != nil {
			t.Fatal(e)
		}
	}
	writeView(oldClient, oldRealm)
	writeView(newClient, newRealm)
	run := &publishRunner{}
	authz := filepath.Join(t.TempDir(), "data.authz")
	p := ServicePublisher{ServiceWC: root, DataAuthzFile: authz, Runner: run}
	repo := uuid.NewString()
	url := "svn+ssh://_filees-client@example/repos/" + repo
	if e := p.Publish(context.Background(), repo, oldRealm, "Docs", url); e != nil {
		t.Fatal(e)
	}
	if e := p.Activate(context.Background(), repo, oldRealm); e != nil {
		t.Fatal(e)
	}

	if err := p.TransferOwner(context.Background(), repo, newRealm); err != nil {
		t.Fatal(err)
	}

	old, _ := clientview.Load(filepath.Join(root, "clients", oldClient, "view.json"))
	if len(old.Repositories) != 0 {
		t.Fatalf("old owner still projects the repository: %+v", old.Repositories)
	}
	fresh, _ := clientview.Load(filepath.Join(root, "clients", newClient, "view.json"))
	if len(fresh.Repositories) != 1 || fresh.Repositories[0].RepoID != repo || fresh.Repositories[0].Access != "rw" || fresh.Repositories[0].OwnerRealmID != newRealm {
		t.Fatalf("new owner projection = %+v", fresh.Repositories)
	}
	raw, _ := os.ReadFile(authz)
	if !strings.Contains(string(raw), newClient) || strings.Contains(string(raw), oldClient) {
		t.Fatalf("authz did not flip owner group: %s", raw)
	}

	// Idempotent no-op: transferring to the already-current owner changes nothing.
	callsBefore := run.calls
	if err := p.TransferOwner(context.Background(), repo, newRealm); err != nil {
		t.Fatal(err)
	}
	if run.calls != callsBefore {
		t.Fatalf("no-op transfer still published: calls=%d, want %d", run.calls, callsBefore)
	}
}

// TestRepositoryRecordPathRefusesEscape is the second-layer half of the audit's
// Finding B. pkg/control/v1 now rejects a non-UUID repo_id on the wire, but
// this is the last point before a client-influenced string becomes a filesystem
// path and it has several callers, so containment must hold on its own rather
// than being inherited from an upstream check.
func TestRepositoryRecordPathRefusesEscape(t *testing.T) {
	const serviceWC = "/srv/filees/service-wc"
	root := filepath.Join(serviceWC, "admin", "repositories")

	for _, repoID := range []string{
		"../../../../etc/passwd",
		"../activation",
		"foo/../../../bar",
		`..\windows`,
		"a/b",
		`a\b`,
		"..",
		".",
		"",
		"a\x00b",
	} {
		if got, err := repositoryRecordPath(serviceWC, repoID); err == nil {
			t.Fatalf("repositoryRecordPath accepted %q -> %q", repoID, got)
		}
	}

	valid := uuid.NewString()
	got, err := repositoryRecordPath(serviceWC, valid)
	if err != nil {
		t.Fatalf("repositoryRecordPath rejected a valid UUID: %v", err)
	}
	if want := filepath.Join(root, valid+".json"); got != want {
		t.Fatalf("repositoryRecordPath = %q, want %q", got, want)
	}
}

// TestActivateRefusesTraversalRepoID drives the same escape attempt through the
// real Activate() entry point, which is where the audit traced the client's
// INITIAL_COMMIT payload landing. The neighbouring record must survive untouched:
// the original defect allowed both a cross-directory read and, on the
// state-change path, a write.
func TestActivateRefusesTraversalRepoID(t *testing.T) {
	root := t.TempDir()
	serviceWC := filepath.Join(root, "service-wc")
	realm := uuid.NewString()
	if err := os.MkdirAll(filepath.Join(serviceWC, "admin", "repositories"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A plausible traversal target: a valid record one directory up.
	outside := filepath.Join(serviceWC, "admin", "secret.json")
	record := repositoryRecord{Schema: RepositorySchema, RepoID: "../secret", OwnerRealmID: realm, State: "initializing", CreatedAt: time.Now().UTC()}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}

	runner := &publishRunner{}
	p := ServicePublisher{ServiceWC: serviceWC, DataAuthzFile: filepath.Join(root, "authz"), Runner: runner}
	if err := p.Activate(context.Background(), "../secret", realm); err == nil {
		t.Fatal("Activate accepted a traversing repo_id")
	}
	after, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("Activate rewrote a record outside the repositories directory")
	}
	if runner.calls != 0 {
		t.Fatalf("Activate published %d change sets for a rejected repo_id", runner.calls)
	}
}
