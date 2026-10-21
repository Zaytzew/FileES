package repoworker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientview"
	"github.com/google/uuid"
)

func archivePolicyFixture(t *testing.T) (ServicePublisher, repositoryRecord, string, string) {
	t.Helper()
	root := t.TempDir()
	p := ServicePublisher{ServiceWC: root, DataAuthzFile: filepath.Join(root, "data.authz"), Runner: &publishRunner{}}
	realm, guest := uuid.NewString(), uuid.NewString()
	ownerClient, guestClient := uuid.NewString(), uuid.NewString()
	for client, realm := range map[string]string{ownerClient: realm, guestClient: guest} {
		if err := atomicJSON(filepath.Join(root, "admin", "realms", realm+".json"), realmRecord{Schema: "filees.realm/v1", RealmID: realm, State: "active", CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		if err := atomicJSON(filepath.Join(root, "admin", "clients", client+".json"), map[string]any{"schema": "filees.client-instance/v1", "client_id": client, "realm_id": realm, "state": "active"}); err != nil {
			t.Fatal(err)
		}
		if _, err := clientview.StoreIfNewer(filepath.Join(root, "clients", client, "view.json"), clientview.View{Schema: clientview.Schema, ServerDisplayName: "Test server", ClientID: client, RealmID: realm, Generation: 1, GeneratedAt: time.Now().UTC(), ClientRole: "normal", Repositories: []clientview.Repository{}}); err != nil {
			t.Fatal(err)
		}
	}
	r := repositoryRecord{Schema: RepositorySchema, RepoID: uuid.NewString(), OwnerRealmID: realm, State: "active", DisplayName: "Archive candidate", URL: "svn+ssh://_filees-data@example.test/repo", CreatedAt: time.Now().UTC()}
	path, _ := repositoryRecordPath(root, r.RepoID)
	if err := atomicJSON(path, r); err != nil {
		t.Fatal(err)
	}
	p.RepositoryHead = func(context.Context, string) (RepositoryRevision, error) {
		return RepositoryRevision{UUID: uuid.NewString(), Number: 7}, nil
	}
	p.WithRealmSessionsStopped = func(_ context.Context, _ string, fn func() error) error { return fn() }
	if _, err := p.Grant(t.Context(), realm, guest, r.RepoID, "rw"); err != nil {
		t.Fatal(err)
	}
	return p, r, ownerClient, guestClient
}

func testArchivePolicy() RepositoryArchivePolicy {
	return RepositoryArchivePolicy{Schema: RepositoryArchivePolicySchema, OperationID: uuid.NewString(), RepositoryUUID: uuid.NewString(), Revision: 7, Epoch: 1, State: "preparing", StartedAt: time.Now().UTC()}
}

func TestArchivePolicyCanonicalProjectionAndEarlyAdmission(t *testing.T) {
	p, r, owner, guest := archivePolicyFixture(t)
	if err := CheckCanonicalRepositoryWrite(p.ServiceWC, r.RepoID); err != nil {
		t.Fatal(err)
	}
	plan := testArchivePolicy()
	for _, phase := range []string{"preparing", "freezing", "archived"} {
		plan.State = phase
		if err := p.publishArchivePolicy(t.Context(), r, plan); err != nil {
			t.Fatal(err)
		}
		for _, client := range []string{owner, guest} {
			v, err := clientview.Load(filepath.Join(p.ServiceWC, "clients", client, "view.json"))
			want := "r"
			if phase == "freezing" {
				want = "rw"
			}
			if err != nil || len(v.Repositories) != 1 || v.Repositories[0].Access != want {
				t.Fatalf("%s %s: %+v %v", phase, client, v, err)
			}
		}
		raw, _ := os.ReadFile(p.DataAuthzFile)
		if phase != "freezing" && (!strings.Contains(string(raw), "owner-"+r.RepoID+" = \n") || !strings.Contains(string(raw), "writer-"+r.RepoID+" = \n") || !strings.Contains(string(raw), owner) || !strings.Contains(string(raw), guest)) {
			t.Fatalf("archive writers remain: %s", raw)
		}
		if !errors.Is(CheckCanonicalRepositoryWrite(p.ServiceWC, r.RepoID), ErrArchiveReadOnly) {
			t.Fatal("canonical admission opened", phase)
		}
		if err := (DumpLoadService{ServiceWC: p.ServiceWC}).checkOwnership(r.RepoID, r.OwnerRealmID); !errors.Is(err, ErrArchiveReadOnly) {
			t.Fatal("dump admission", err)
		}
		if _, err := p.Grant(t.Context(), r.OwnerRealmID, uuid.NewString(), r.RepoID, "rw"); !errors.Is(err, ErrArchiveReadOnly) {
			t.Fatal("rw grant", err)
		}
		if err := p.TransferOwner(t.Context(), r.RepoID, uuid.NewString()); !errors.Is(err, ErrArchiveReadOnly) {
			t.Fatal("transfer", err)
		}
		if _, err := p.SetRepositoryEditingPolicy(t.Context(), r.OwnerRealmID, r.RepoID, "lock_required"); !errors.Is(err, ErrArchiveReadOnly) {
			t.Fatal("editing policy", err)
		}
		ownerView, err := clientview.Load(filepath.Join(p.ServiceWC, "clients", owner, "view.json"))
		if err != nil {
			t.Fatal(err)
		}
		session := Session{ClientID: owner, RealmID: r.OwnerRealmID, Repositories: ownerView.Repositories}
		session.Repositories[0].Access = "rw" // stale projection before canonical freeze
		passport := PassportReplacementAuthority{ServiceWC: p.ServiceWC}
		if err := passport.authorizeAccess(session, r.RepoID, true); !errors.Is(err, ErrArchiveReadOnly) {
			t.Fatal("passport replacement", err)
		}
		if err := passport.authorizeAccess(session, r.RepoID, false); err != nil {
			t.Fatal("ownership read blocked", err)
		}
		err = p.AuthorizeDelete(t.Context(), r.RepoID, r.OwnerRealmID)
		if phase != "archived" && !errors.Is(err, ErrArchiveReadOnly) {
			t.Fatal("pending deletion", err)
		}
		if phase == "archived" && err != nil {
			t.Fatal("explicit lifecycle deletion must retain its own authority", err)
		}
	}
	// A disposable rw view cannot reopen canonical writes.
	if err := p.RebuildGrantAuthority(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(CheckCanonicalRepositoryWrite(p.ServiceWC, r.RepoID), ErrArchiveReadOnly) {
		t.Fatal("rebuild opened archive")
	}
}

func TestArchivePolicyMalformedAuthorityFailsClosed(t *testing.T) {
	for _, phase := range []string{"released", "unknown", ""} {
		p, r, _, _ := archivePolicyFixture(t)
		plan := testArchivePolicy()
		plan.State = phase
		r.ArchivePolicy = &plan
		path, _ := repositoryRecordPath(p.ServiceWC, r.RepoID)
		if err := atomicJSON(path, r); err != nil {
			t.Fatal(err)
		}
		if CheckCanonicalRepositoryWrite(p.ServiceWC, r.RepoID) == nil || p.Activate(t.Context(), r.RepoID, r.OwnerRealmID) == nil || p.RebuildGrantAuthority(t.Context()) == nil {
			t.Fatal("invalid authority accepted", phase)
		}
	}
	if CheckCanonicalRepositoryWrite(t.TempDir(), uuid.NewString()) == nil {
		t.Fatal("missing authority accepted")
	}
}
