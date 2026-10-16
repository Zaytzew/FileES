package servertool

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/internal/svntool"
	"filees/internal/svnurl"
	"filees/pkg/clientview"
	"filees/pkg/repoworker"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

// Real SVN source, no network mutations or user data. Exercises the actual
// production builder, canonical authority and cached origins across lock edits.
func TestReservationBatchRealSVNLockWithoutCommitAndRevoke(t *testing.T) {
	if reason, mustFail := svntool.Check("svn", "svnadmin"); reason != "" {
		if mustFail {
			t.Fatal(reason)
		}
		t.Skip(reason)
	}
	svn, _ := svntool.Lookup("svn")
	admin, _ := svntool.Lookup("svnadmin")
	root := t.TempDir()
	repoID, realm, clientID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	c := serverconfig.Config{ServerID: "lab"}
	c.Activation.Root = filepath.Join(root, "activation")
	c.Activation.ServiceWorkingCopy = filepath.Join(root, "service")
	c.Activation.SVNBinary = svn
	c.Repositories.Root = filepath.Join(root, "repos")
	artifacts := filepath.Join(root, "artifacts")
	repo := filepath.Join(c.Repositories.Root, repoID)
	wc := filepath.Join(root, "wc")
	for _, dir := range []string{c.Activation.Root, c.Repositories.Root, artifacts} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	run := func(bin string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), bin, args...)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("SVN fixture: %v %s", err, out)
		}
	}
	run(admin, "create", repo)
	run(svn, "co", svnurl.File(repo), wc)
	file := filepath.Join(wc, "zażółć.txt")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	run(svn, "add", file)
	run(svn, "ci", "--username", clientID, "-m", "fixture", wc)
	put := func(rel string, v any) {
		t.Helper()
		p := filepath.Join(c.Activation.ServiceWorkingCopy, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(v)
		if err := os.WriteFile(p, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	put("admin/clients/"+clientID+".json", map[string]any{"schema": "filees.client-instance/v1", "client_id": clientID, "realm_id": realm, "state": "active"})
	put("admin/realms/"+realm+".json", map[string]any{"schema": "filees.realm/v1", "realm_id": realm, "state": "active"})
	put("admin/repositories/"+repoID+".json", map[string]any{"schema": repoworker.RepositorySchema, "repo_id": repoID, "owner_realm_id": realm, "state": "active"})
	v := clientview.View{Schema: clientview.Schema, ClientID: clientID, RealmID: realm, ClientRole: "normal", ServerDisplayName: "Lab", Generation: 1, GeneratedAt: time.Now(), Repositories: []clientview.Repository{{RepoID: repoID, State: "active", Access: "rw", DisplayName: "Fixture", URL: "svn+ssh://_filees-data@example/" + repoID, OwnerRealmID: realm}}}
	if _, err := clientview.StoreIfNewer(filepath.Join(c.Activation.ServiceWorkingCopy, "clients", clientID, "view.json"), v); err != nil {
		t.Fatal(err)
	}
	req := reservationv1.BatchRequest{Schema: reservationv1.BatchSchema, Scope: reservationv1.BatchScope("lab", v), Known: map[string]string{}}
	cache := map[string]reservationv1.Result{}
	read := func(changed bool) reservationv1.Result {
		t.Helper()
		raw, err := reservationBatch(filepath.Join(root, "server.json"), c, clientID, req, artifacts)
		if err != nil {
			t.Fatal(err)
		}
		response, err := reservationv1.ParseBatchResponse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if (response.Entries[0].Result != nil) != changed {
			t.Fatal("wrong delta")
		}
		cache, err = reservationv1.ApplyBatch(req, response, "lab", clientID, realm, cache)
		if err != nil {
			t.Fatal(err)
		}
		req.Known[repoID] = reservationv1.BatchVersion(req.Scope, cache[repoID])
		return cache[repoID]
	}
	first := read(true)
	if first.PathOwnership == nil || first.PathOwnership.Revision != 1 {
		t.Fatal("missing real snapshot")
	}
	read(false)
	run(svn, "lock", "--username", clientID, "-m", "fixture", file)
	locked := read(true)
	if len(locked.Reservations) != 1 || locked.Reservations[0].Path != "zażółć.txt" || locked.PathOwnership.Revision != 1 {
		t.Fatal("lock without commit not observed")
	}
	run(svn, "unlock", "--username", clientID, file)
	if len(read(true).Reservations) != 0 {
		t.Fatal("unlock not observed")
	}
	put("admin/clients/"+clientID+".json", map[string]any{"schema": "filees.client-instance/v1", "client_id": clientID, "realm_id": realm, "state": "revoked"})
	if raw, err := reservationBatch(filepath.Join(root, "server.json"), c, clientID, req, artifacts); err == nil || len(raw) != 0 {
		t.Fatal("revocation survived unchanged displayed view")
	}
}
