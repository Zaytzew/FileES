package servertool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientview"
	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

func TestArchivePolicyStaleRWViewCannotAuthorizeWhaleOrMobileWrite(t *testing.T) {
	root, repo, client, realm := t.TempDir(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	v := clientview.View{Schema: clientview.Schema, ServerDisplayName: "Test server", ClientID: client, RealmID: realm, Generation: 1, GeneratedAt: time.Now().UTC(), ClientRole: "normal", Repositories: []clientview.Repository{{RepoID: repo, DisplayName: "Archive", URL: "svn+ssh://_filees-data@example.test/" + repo, Access: "rw", State: "active"}}}
	if _, err := clientview.StoreIfNewer(filepath.Join(root, "clients", client, "view.json"), v); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "admin", "repositories", repo+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"ordinary", "preparing", "freezing", "archived", "invalid", "missing"} {
		record := map[string]any{"schema": repoworker.RepositorySchema, "repo_id": repo, "owner_realm_id": realm, "state": "active"}
		if phase != "ordinary" {
			record["archive_policy"] = repoworker.RepositoryArchivePolicy{Schema: repoworker.RepositoryArchivePolicySchema, OperationID: uuid.NewString(), RepositoryUUID: uuid.NewString(), Revision: 2, Epoch: 1, State: phase, StartedAt: time.Now().UTC()}
		}
		raw, _ := json.Marshal(record)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if phase == "missing" {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		mobile, err := (clientviewMobileAuthority{ServiceWorkingCopy: root, RepositoriesRoot: root}).Resolve(t.Context(), client, repo)
		if err != nil {
			t.Fatal("authorized read lost", err)
		}
		whale, err := (clientviewWhaleAuthority{ServiceWC: root, RepositoriesRoot: root}).ResolveWhale(t.Context(), client, repo)
		if err != nil {
			t.Fatal("authorized read lost", err)
		}
		want := "r"
		if phase == "ordinary" {
			want = "rw"
		}
		if mobile.Access != want || whale.Access != want {
			t.Fatal(phase, mobile.Access, whale.Access)
		}
	}
}
