package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"filees/pkg/client"
	"filees/pkg/clientprofile"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
	"fmt"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
)

func TestShelfFirstDownloadCreatesNewFolderAndReusesRecordedPath(t *testing.T) {
	root := t.TempDir()
	store, err := localrepo.Open(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := repositoryLifecycleService{store: store, onCreate: func(string) {}}
	item := contract.ShelfItem{UploadID: "upload", RepoPath: "file.txt", SHA256: fmt.Sprintf("%x", sha256.Sum256(nil))}
	path := filepath.Join(root, "new-shelf")
	for _, existing := range []string{root, filepath.Join(root, "empty")} {
		if existing != root {
			if err := os.Mkdir(existing, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := service.BeginShelfFetch("office", uuid.NewString(), "svn+ssh://example/shelf", existing, item); !errors.Is(err, os.ErrExist) {
			t.Fatalf("existing target: %v", err)
		}
	}
	result, err := service.BeginShelfFetch("office", uuid.NewString(), "svn+ssh://example/shelf", path, item)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("folder not created: %v", err)
	}
	record, ok := store.Get(result.OperationID)
	if !ok || record.LocalPath != path {
		t.Fatalf("path not recorded: %+v", record)
	}
	if got := service.InspectShelf(record.ServerID, record.RepoID); got.LocalPath != path {
		t.Fatalf("inspect=%+v", got)
	}
	if _, err := service.BeginShelfFetch(record.ServerID, record.RepoID, record.RepoURL, path, item); errors.Is(err, os.ErrExist) {
		t.Fatal("recorded path treated as a new folder")
	}
}

func TestShelfFolderIconHasDistinctArtwork(t *testing.T) {
	icon, err := shelfFolderIconBytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(icon) == 0 || bytes.Equal(icon, managedFolderIcon) {
		t.Fatal("shelf icon is not distinct")
	}
}

type shelfSVNStub struct {
	attachmentSVNStub
	fetched int
	bytes   []byte
}

func (s *shelfSVNStub) FetchSparsePath(_ context.Context, root, path string) (string, error) {
	s.fetched++
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	return "", os.WriteFile(path, s.bytes, 0600)
}

func TestShelfDownloadFromFreshAttachmentAndRestartReceipt(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		t.Run(fmt.Sprint(dirty), func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "local.json")
			store, err := localrepo.Open(statePath)
			if err != nil {
				t.Fatal(err)
			}
			r, err := store.BeginShelfAttach("office", uuid.NewString(), "svn+ssh://client@example/shelf", filepath.Join(t.TempDir(), "shelf"))
			if err != nil {
				t.Fatal(err)
			}
			data := []byte("selected file")
			r, err = store.QueueShelfFetch(r.OperationID, "upload", "incoming/deep/file.txt", fmt.Sprintf("%x", sha256.Sum256(data)), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			svn := &shelfSVNStub{bytes: data}
			p := newDaemonProvisioner(store, nil, nil)
			p.attachments = make(chan provisionedAttachment, 3)
			p.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN { return svn }
			profile := clientprofile.Profile{ServerID: "office"}
			p.runAttach(t.Context(), r, profile)
			r, _ = store.Get(r.OperationID)
			if dirty {
				svn.status = []client.StatusEntry{{Path: filepath.Join(r.LocalPath, "file.txt"), Item: "modified"}}
			}
			p.runShelfFetch(t.Context(), r, profile)
			store, err = localrepo.Open(statePath)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := store.Get(r.OperationID)
			if dirty {
				if got.ShelfFetch.State != "failed" || svn.fetched != 0 {
					t.Fatalf("dirty transfer: %+v", got)
				}
				return
			}
			if got.ShelfFetch.State != "complete" || svn.fetched != 1 || svn.checkout != 0 || svn.sparse != 1 {
				t.Fatalf("receipt=%+v SVN=%+v", got, svn)
			}
			if _, err := os.Stat(filepath.Join(r.LocalPath, "incoming/deep/file.txt")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestShelfImportRecoveredNameAndDestinationGuards(t *testing.T) {
	for _, tc := range []struct {
		name, original, repoPath, destination, existing, want string
	}{
		{name: "recovered", repoPath: "incoming/20260929_191648-1.jpg", want: "20260929_191648-1.jpg"},
		{name: "original Unicode", original: "Zdjęcie.jpg", repoPath: "incoming/server-name.jpg", want: "Zdjęcie.jpg"},
		{name: "unsafe original", original: "../bad.jpg", repoPath: "incoming/safe.jpg"},
		{name: "unsafe recovered path", repoPath: "incoming/../safe.jpg"},
		{name: "missing names"},
		{name: "reserved name", repoPath: "incoming/CON.jpg"},
		{name: "existing file", repoPath: "incoming/photo.jpg", existing: "photo.jpg"},
		{name: "case collision", repoPath: "incoming/photo.jpg", existing: "Photo.jpg"},
		{name: "outside parent", repoPath: "incoming/photo.jpg", destination: "outside"},
		{name: "nested WC", repoPath: "incoming/photo.jpg", destination: "nested"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			parentRoot := filepath.Join(root, "parent")
			if err := os.Mkdir(parentRoot, 0700); err != nil {
				t.Fatal(err)
			}
			store, err := localrepo.Open(filepath.Join(root, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			started := 0
			service := repositoryLifecycleService{store: store, onCreate: func(string) { started++ }}
			parent := contract.RepoSummary{ID: uuid.NewString(), URL: "svn+ssh://example/parent", LocalPath: parentRoot}
			item := contract.ShelfItem{UploadID: "recovered-upload", OriginalName: tc.original, RepoPath: tc.repoPath, SHA256: fmt.Sprintf("%x", sha256.Sum256(nil))}
			destination := parentRoot
			if tc.existing != "" {
				if err := os.WriteFile(filepath.Join(parentRoot, tc.existing), []byte("user data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.destination == "outside" {
				destination = root
			}
			if tc.destination == "nested" {
				destination = filepath.Join(parentRoot, "nested")
				if err := os.MkdirAll(filepath.Join(destination, ".svn"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			shelfPath := filepath.Join(root, "shelf")
			result, err := service.BeginShelfImport("office", uuid.NewString(), "svn+ssh://example/shelf", shelfPath, item, parent, destination)
			if tc.want == "" {
				if tc.name == "existing file" && !errors.Is(err, os.ErrExist) {
					t.Fatalf("collision lost its error class: %v", err)
				}
				if err == nil || started != 0 || len(store.List()) != 0 {
					t.Fatalf("rejected import had effects: %v starts=%d", err, started)
				}
				if _, err := os.Stat(shelfPath); !os.IsNotExist(err) {
					t.Fatal("rejected import created a shelf")
				}
				if tc.existing != "" {
					data, err := os.ReadFile(filepath.Join(parentRoot, tc.existing))
					if err != nil || string(data) != "user data" {
						t.Fatal("existing file changed")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, ok := store.Get(result.OperationID)
			if !ok || started != 1 || got.ShelfFetch.State != "queued" || got.ShelfFetch.Placement.RelativePath != tc.want || got.ShelfFetch.RepoPath != tc.repoPath {
				t.Fatalf("wrong durable import: %+v starts=%d", got, started)
			}
			if item.OriginalName != tc.original {
				t.Fatal("receipt original name changed")
			}
		})
	}
}

func TestShelfImportPreparesPrivateCacheOnlyWhenNeeded(t *testing.T) {
	dataRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := repositoryLifecycleService{store: store, onCreate: func(string) {}}
	parent := contract.RepoSummary{ID: uuid.NewString(), URL: "svn+ssh://example/parent", LocalPath: t.TempDir()}
	item := contract.ShelfItem{UploadID: "upload", RepoPath: "photo.jpg", SHA256: fmt.Sprintf("%x", sha256.Sum256(nil))}
	repoID := uuid.NewString()
	result, err := service.BeginShelfImport("office", repoID, "svn+ssh://example/shelf", "", item, parent, parent.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := store.Get(result.OperationID)
	cache := filepath.Join(dataRoot, "filees", "shelf-cache")
	if filepath.Dir(record.LocalPath) != cache || record.ShelfFetch.Placement.ParentRoot != parent.LocalPath {
		t.Fatalf("wrong cache or destination: %+v", record)
	}
	if info, err := os.Stat(record.LocalPath); err != nil || !info.IsDir() {
		t.Fatalf("cache missing: %v", err)
	}
	if _, err := store.MarkAttached(record.OperationID, repoID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetShelfFetchState(record.OperationID, record.ShelfFetch.ID, "complete", nil); err != nil {
		t.Fatal(err)
	}
	reused, err := service.BeginShelfImport("office", repoID, "svn+ssh://example/shelf", "", item, parent, parent.LocalPath)
	if err != nil || reused.OperationID != record.OperationID || reused.LocalPath != record.LocalPath {
		t.Fatalf("cache not reused: %+v %v", reused, err)
	}
	other, err := service.BeginShelfImport("other-server", repoID, "svn+ssh://other/shelf", "", item, parent, parent.LocalPath)
	if err != nil || other.LocalPath == record.LocalPath {
		t.Fatalf("different servers share cache: %+v %v", other, err)
	}
}

func TestShelfAutomaticCacheDoesNotAdoptUnrelatedFolder(t *testing.T) {
	dataRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := repositoryLifecycleService{store: store, onCreate: func(string) {}}
	parent := contract.RepoSummary{ID: uuid.NewString(), URL: "svn+ssh://example/parent", LocalPath: t.TempDir()}
	item := contract.ShelfItem{UploadID: "upload", RepoPath: "photo.jpg", SHA256: fmt.Sprintf("%x", sha256.Sum256(nil))}
	repoID := uuid.NewString()
	id := sha256.Sum256([]byte("office\x00" + repoID))
	path := filepath.Join(dataRoot, "filees", "shelf-cache", fmt.Sprintf("%x", id))
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(path, "user.txt")
	if err := os.WriteFile(userFile, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BeginShelfImport("office", repoID, "svn+ssh://example/shelf", "", item, parent, parent.LocalPath); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing folder accepted: %v", err)
	}
	if len(store.List()) != 0 {
		t.Fatal("unrelated cache folder recorded as an attachment")
	}
	data, err := os.ReadFile(userFile)
	if err != nil || string(data) != "preserve" {
		t.Fatal("unrelated data changed")
	}
}

func TestShelfAutomaticCacheRejectsInvalidSelectionAndWorkingCopyOverlap(t *testing.T) {
	for _, overlap := range []bool{false, true} {
		t.Run(fmt.Sprint(overlap), func(t *testing.T) {
			dataRoot := t.TempDir()
			t.Setenv("XDG_DATA_HOME", dataRoot)
			store, err := localrepo.Open(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			roots := []string{}
			if overlap {
				roots = append(roots, dataRoot)
			}
			started := 0
			service := repositoryLifecycleService{store: store, existingRoots: roots, onCreate: func(string) { started++ }}
			parent := contract.RepoSummary{ID: uuid.NewString(), URL: "svn+ssh://example/parent", LocalPath: t.TempDir()}
			item := contract.ShelfItem{UploadID: "upload", RepoPath: "photo.jpg", SHA256: fmt.Sprintf("%x", sha256.Sum256(nil))}
			if !overlap {
				item.SHA256 = "not-a-checksum"
			}
			if _, err := service.BeginShelfImport("office", uuid.NewString(), "svn+ssh://example/shelf", "", item, parent, parent.LocalPath); err == nil {
				t.Fatal("invalid automatic import accepted")
			}
			if len(store.List()) != 0 || started != 0 {
				t.Fatal("rejected import queued work")
			}
			if _, err := os.Stat(filepath.Join(dataRoot, "filees", "shelf-cache")); !os.IsNotExist(err) {
				t.Fatal("rejected import created cache under data root")
			}
		})
	}
}
