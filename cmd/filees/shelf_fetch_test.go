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
