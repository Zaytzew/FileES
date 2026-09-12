package main

import (
	"context"
	"crypto/sha256"
	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/localrepo"
	"fmt"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
)

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
