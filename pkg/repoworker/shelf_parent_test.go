package repoworker

import (
	"encoding/json"
	"filees/pkg/clientview"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
)

func TestShelfRebuildPersistsPromotionWhenParentAlreadyKnown(t *testing.T) {
	root, public := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "admin", "clients"), 0700); err != nil {
		t.Fatal(err)
	}
	parent, shelf, owner := uuid.NewString(), uuid.NewString(), uuid.NewString()
	shelfPath, _ := repositoryRecordPath(root, shelf)
	parentPath, _ := repositoryRecordPath(root, parent)
	for path, record := range map[string]repositoryRecord{
		parentPath: {Schema: RepositorySchema, RepoID: parent, OwnerRealmID: owner, State: "active"},
		shelfPath:  {Schema: RepositorySchema, RepoID: shelf, OwnerRealmID: owner, State: "initializing", Purpose: clientview.PurposeUploadShelf, ParentRepoID: parent},
	} {
		if err := atomicJSON(path, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := atomicJSON(filepath.Join(public, "upload-channels", "channel.json"), map[string]any{"schema": "filees.upload-channel/v1", "owner_realm": owner, "state": "active", "manifest": map[string]string{"authority_repo_id": parent, "upload_repo_id": shelf}}); err != nil {
		t.Fatal(err)
	}
	p := ServicePublisher{ServiceWC: root, PublicShareStateRoot: public, DataAuthzFile: filepath.Join(root, "authz")}
	if _, err := p.rebuildGrantAuthority(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(shelfPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved repositoryRecord
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.State != "active" || saved.ParentRepoID != parent {
		t.Fatalf("record=%+v", saved)
	}
}

func TestShelfParentComesOnlyFromOwnedManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "upload-channels")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// No name/slug relation: the IDs alone bind these differently named repos.
	repos := map[string]repositoryRecord{
		"parent": {RepoID: "parent", OwnerRealmID: "owner", DisplayName: "Project"},
		"shelf":  {RepoID: "shelf", OwnerRealmID: "owner", DisplayName: "Unrelated name", Purpose: clientview.PurposeUploadShelf},
	}
	p := ServicePublisher{PublicShareStateRoot: root}
	if err := p.projectShelfParents(repos); err != nil {
		t.Fatal(err)
	}
	if repos["shelf"].ParentRepoID != "" {
		t.Fatal("invented parent")
	}
	if err := os.WriteFile(filepath.Join(dir, "record.json"), []byte(`{"schema":"filees.upload-channel/v1","owner_realm":"owner","state":"active","manifest":{"authority_repo_id":"parent","upload_repo_id":"shelf"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.projectShelfParents(repos); err != nil {
		t.Fatal(err)
	}
	if repos["shelf"].ParentRepoID != "parent" {
		t.Fatal("parent missing")
	}
	for _, tc := range []struct{ parent, shelf, want string }{
		{"active", "initializing", "active"},
		{"active", "deleted", "deleted"},
		{"deleted", "initializing", "initializing"},
		{"initializing", "initializing", "initializing"},
	} {
		parent, shelf := repos["parent"], repos["shelf"]
		parent.State, shelf.State = tc.parent, tc.shelf
		repos["parent"], repos["shelf"] = parent, shelf
		if err := p.projectShelfParents(repos); err != nil {
			t.Fatal(err)
		}
		if repos["shelf"].State != tc.want {
			t.Fatalf("%+v: state=%s", tc, repos["shelf"].State)
		}
	}
	parent := repos["parent"]
	parent.OwnerRealmID = "other"
	repos["parent"] = parent
	if err := p.projectShelfParents(repos); err == nil {
		t.Fatal("cross-realm binding accepted")
	}
}
