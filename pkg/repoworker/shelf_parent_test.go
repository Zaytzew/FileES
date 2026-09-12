package repoworker

import (
	"filees/pkg/clientview"
	"os"
	"path/filepath"
	"testing"
)

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
	parent := repos["parent"]
	parent.OwnerRealmID = "other"
	repos["parent"] = parent
	if err := p.projectShelfParents(repos); err == nil {
		t.Fatal("cross-realm binding accepted")
	}
}
