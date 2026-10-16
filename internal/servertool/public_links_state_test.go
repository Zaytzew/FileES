package servertool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientview"
	"filees/pkg/publicstate"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"filees/public-shares/channel"
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

func TestPublicLinksStateCanonicalRecordsInitDeltaAndRevocation(t *testing.T) {
	c := serverconfig.Config{ServerID: "lab"}
	c.Activation.Root = t.TempDir()
	c.Activation.ServiceWorkingCopy = t.TempDir()
	c.Repositories.ResultsRoot = t.TempDir()
	c.PublicShares.Enabled = true
	c.PublicShares.BaseURL = "https://public.example"
	clientID, realm, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	put := func(path string, v any) {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(c.Activation.ServiceWorkingCopy, "admin", "repositories", repoID+".json"), map[string]any{"schema": repoworker.RepositorySchema, "repo_id": repoID, "owner_realm_id": realm, "state": "active"})
	put(filepath.Join(c.Activation.ServiceWorkingCopy, "admin", "realms", realm+".json"), map[string]any{"schema": "filees.realm/v1", "realm_id": realm, "state": "active", "alias": "lab"})
	view := clientview.View{Schema: clientview.Schema, ServerDisplayName: "Lab", ClientID: clientID, RealmID: realm, Generation: 1, GeneratedAt: time.Now().UTC(), ClientRole: "normal", Repositories: []clientview.Repository{{RepoID: repoID, DisplayName: "Remote name", URL: "svn+ssh://_filees-data@example/" + repoID, Access: "rw", State: "active", OwnerRealmID: realm}}}
	viewPath := filepath.Join(c.Activation.ServiceWorkingCopy, "clients", clientID, "view.json")
	if _, err := clientview.StoreIfNewer(viewPath, view); err != nil {
		t.Fatal(err)
	}
	store := &channel.Store{Root: c.PublicShares.EffectiveStateRoot(c.Repositories.ResultsRoot), Authority: repoworker.ServicePublisher{ServiceWC: c.Activation.ServiceWorkingCopy}, TokenKey: []byte(strings.Repeat("k", 32))}
	id := uuid.NewString()
	declaration := manifest.Share{OwnerRealm: realm, RepoID: repoID, Slug: "project", SourceRoot: "docs", Recipients: []string{"reader@example.com"}, Objects: []manifest.Object{{PublicID: "7f3a1c9e2b4d6a80", RepoPath: "docs/a.txt", DisplayName: "a.txt"}}}
	_, deliveries, err := store.Create(id, realm, declaration)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot publicstate.Snapshot
	raw, err := publicLinksState(c, clientID, snapshot.Request())
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || strings.Contains(string(raw), deliveries[0].Token) || strings.Contains(string(raw), "token_sha256") || strings.Contains(string(raw), "invitation_message") {
		t.Fatal("credential or editor payload leaked")
	}
	r, err := publicstate.ParseResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	b := publicstate.Binding{ServerID: "lab", ClientID: clientID, RealmID: realm}
	snapshot, err = snapshot.Apply(snapshot.Request(), r, b)
	if err != nil || len(snapshot.Items) != 1 {
		t.Fatalf("init: %v", err)
	}
	raw, err = publicLinksState(c, clientID, snapshot.Request())
	if err != nil {
		t.Fatal(err)
	}
	r, err = publicstate.ParseResponse(raw)
	if err != nil || len(r.Changes) != 0 {
		t.Fatalf("unchanged: %v", err)
	}
	if _, err = store.Revoke(realm, id); err != nil {
		t.Fatal(err)
	}
	raw, err = publicLinksState(c, clientID, snapshot.Request())
	if err != nil {
		t.Fatal(err)
	}
	r, err = publicstate.ParseResponse(raw)
	if err != nil || len(r.Changes) != 1 || r.Changes[0].Share.State != "revoked" {
		t.Fatalf("revoke: %v", err)
	}
	view.Generation++
	view.GeneratedAt = time.Now().UTC()
	view.Repositories = nil
	if _, err = clientview.StoreIfNewer(viewPath, view); err != nil {
		t.Fatal(err)
	}
	raw, err = publicLinksState(c, clientID, snapshot.Request())
	if err != nil {
		t.Fatal(err)
	}
	r, err = publicstate.ParseResponse(raw)
	if err != nil || len(r.Versions) != 0 {
		t.Fatalf("withdrawn scope: %v", err)
	}
	if _, err = publicLinksState(c, uuid.NewString(), snapshot.Request()); err == nil {
		t.Fatal("unknown client accepted")
	}
}
