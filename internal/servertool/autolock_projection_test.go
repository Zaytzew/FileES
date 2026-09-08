package servertool

import (
	"encoding/json"
	"filees/internal/svnurl"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/activation"
	"filees/pkg/clientview"
	"filees/pkg/repoworker"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func TestAutolockBrokerRealHistoryAndCanonicalRevocation(t *testing.T) {
	tools := requireSVN(t, "svnadmin", "svn")
	svnadmin, svn := tools[0], tools[1]
	root := t.TempDir()
	repoID, owner, guest, clientID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	repos, service, artifacts := filepath.Join(root, "repos"), filepath.Join(root, "service"), filepath.Join(root, "artifacts")
	if err := os.MkdirAll(repos, 0700); err != nil {
		t.Fatal(err)
	}
	repo, wc := filepath.Join(repos, repoID), filepath.Join(root, "wc")
	run := func(bin string, args ...string) {
		t.Helper()
		if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v %s", bin, args, err, out)
		}
	}
	run(svnadmin, "create", repo)
	run(svn, "co", svnurl.File(repo), wc)
	file := filepath.Join(wc, "guest.txt")
	if err := os.WriteFile(file, []byte("guest data"), 0644); err != nil {
		t.Fatal(err)
	}
	run(svn, "add", file)
	run(svn, "ci", "--username", clientID, "-m", "guest creates", wc)
	put := func(rel string, value any) {
		t.Helper()
		p := filepath.Join(service, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(p, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	put("admin/clients/"+clientID+".json", map[string]any{"schema": "filees.client-instance/v1", "client_id": clientID, "realm_id": guest, "state": "active"})
	for _, r := range []string{owner, guest} {
		put("admin/realms/"+r+".json", map[string]any{"schema": "filees.realm/v1", "realm_id": r, "state": "active"})
	}
	put("admin/repositories/"+repoID+".json", map[string]any{"schema": repoworker.RepositorySchema, "repo_id": repoID, "owner_realm_id": owner, "state": "active", "display_name": "test", "url": "svn+ssh://_filees-data@example/" + repoID, "created_at": time.Now()})
	head, err := (repoworker.SVNPathOwners{SVN: svn, RepositoriesRoot: repos, ServiceWC: service}).Head(t.Context(), repoID)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := int64(0)
	grant := repoworker.RealmGrantRecord{Schema: repoworker.RealmGrantSchema, RepoID: repoID, OwnerRealmID: owner, RecipientRealmID: guest, Access: "rw", State: "active", PathOwnerPolicy: "first_committer", PathOwnerCutoffRevision: &cutoff, PathOwnerRepositoryUUID: head.UUID, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	grantPath := "admin/grants/" + repoID + "/" + guest + ".json"
	put(grantPath, grant)
	view := clientview.View{Schema: clientview.Schema, ClientID: clientID, RealmID: guest, ClientRole: "normal", ServerDisplayName: "fixture", Generation: 1, GeneratedAt: time.Now(), Repositories: []clientview.Repository{reservationTestRepository(repoID, "active")}}
	if _, err := clientview.StoreIfNewer(filepath.Join(service, "clients", clientID, "view.json"), view); err != nil {
		t.Fatal(err)
	}
	config := serverconfig.Config{Activation: activation.Config{ServiceWorkingCopy: service, SVNBinary: svn}, Repositories: serverconfig.RepositoryFile{Root: repos}}
	req := reservationv1.Request{Schema: reservationv1.AutolockSchema, RepoID: repoID}
	read := func() reservationv1.Result {
		t.Helper()
		r, err := refreshAutolockProjection(t.Context(), config, clientID, req, artifacts)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(r)
		if _, err := reservationv1.ParseResult(raw); err != nil {
			t.Fatalf("wire: %v, %s", err, raw)
		}
		return r
	}
	first := read()
	if first.PathOwnership == nil || len(first.PathOwnership.Entries) != 1 || first.PathOwnership.Entries[0].OwnerRealmID != guest {
		t.Fatalf("projection: %+v", first)
	}
	// Edit the displayed realm in the lock comment. Ownership must still
	// come from the authenticated SVN author, never this client-controlled text.
	run(svn, "lock", "--username", clientID, "-m", "realm="+owner, file)
	locked := read()
	if len(locked.Reservations) != 1 || locked.Reservations[0].OwnerRealmID != guest {
		t.Fatalf("canonical holder: %+v", locked.Reservations)
	}
	// Same HEAD, populated history cache, but revoke must take effect now.
	grant.State = "revoked"
	put(grantPath, grant)
	if _, err := refreshAutolockProjection(t.Context(), config, clientID, req, artifacts); err == nil {
		t.Fatal("stale view survived grant revoke")
	}
	// Restore read access only: operational ownership now belongs to repo owner.
	grant.State = "active"
	grant.Access = "r"
	grant.PathOwnerPolicy = ""
	put(grantPath, grant)
	after := read()
	if after.PathOwnership == nil || after.PathOwnership.Entries[0].OwnerRealmID != owner || after.PathOwnership.Entries[0].Object != first.PathOwnership.Entries[0].Object {
		t.Fatalf("cached rights survived revoke: %+v", after)
	}
}
