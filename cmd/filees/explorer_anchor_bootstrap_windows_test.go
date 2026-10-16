//go:build windows && !nocfapi

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/client"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/talk"
)

type anchorBootstrapReader struct {
	client.Client
	t           *testing.T
	failListing bool
	revision    int64
}

func (r anchorBootstrapReader) Revision(context.Context, string) (int64, error) {
	return 0, errors.New("HEAD temporarily unavailable")
}

func (r anchorBootstrapReader) HistoryListTree(_ context.Context, _ string, revision int64, out string) (client.HistoryTreeSummary, error) {
	if revision != r.revision {
		r.t.Fatalf("baseline not pinned to seed: %d", revision)
	}
	if r.failListing {
		return client.HistoryTreeSummary{}, errors.New("seed listing unavailable")
	}
	body := ""
	if revision != 0 {
		body = "{\"path\":\"file.txt\",\"kind\":\"file\",\"size\":12}\n"
	}
	return client.HistoryTreeSummary{Revision: revision}, os.WriteFile(out, []byte(body), 0600)
}

func TestAnchorBootstrapPersistsSeedBeforeHEADAndFailsClosed(t *testing.T) {
	for _, mode := range []string{"head-offline", "listing-failed", "bad-seed", "corrupt-tree", "zero-head-offline", "zero-listing-failed", "zero-missing", "zero-null", "zero-nonempty"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			point := filepath.Join(root, "point")
			if err := os.MkdirAll(filepath.Join(point, ".filees"), 0700); err != nil {
				t.Fatal(err)
			}
			store, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.BeginAnchorAttach("server", "repo", point, false)
			if err != nil {
				t.Fatal(err)
			}
			url := "svn+ssh://host/repo"
			if _, err = store.ApproveAttach(record.OperationID, "server", "repo", url, "rw"); err != nil {
				t.Fatal(err)
			}
			record, err = store.MarkAttached(record.OperationID, "repo")
			if err != nil {
				t.Fatal(err)
			}
			server := ipcserver.New("unused")
			server.RegisterRepoAccess("repo", url, point, "server", "rw")
			seed := `{"schema":"filees.anchor-seed/v1","revision":7,"files":1}`
			baseline := int64(7)
			if strings.HasPrefix(mode, "zero-") {
				baseline = 0
				seed = `{"schema":"filees.anchor-seed/v1","revision":0}`
			}
			switch mode {
			case "zero-missing":
				seed = `{"schema":"filees.anchor-seed/v1"}`
			case "zero-null":
				seed = `{"schema":"filees.anchor-seed/v1","revision":null}`
			case "zero-nonempty":
				seed = `{"schema":"filees.anchor-seed/v1","revision":0,"files":1}`
			}
			if mode == "bad-seed" {
				seed = `{"schema":"unknown","revision":7}`
			}
			if err = os.WriteFile(anchorSeedPath(point), []byte(seed), 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "corrupt-tree" {
				if err = os.WriteFile(anchorTreePath(point), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Local content is not evidence of remote cleanliness and must not
			// be inspected or changed by metadata-only baseline recovery.
			local := filepath.Join(point, "file.txt")
			if err = os.WriteFile(local, []byte("unpublished local edit"), 0600); err != nil {
				t.Fatal(err)
			}
			m := &anchorManager{lifecycle: store, repos: server.RepoState, log: talk.With("bootstrap-test"),
				newSVN: func(string) (client.Client, error) {
					return anchorBootstrapReader{t: t, revision: baseline, failListing: strings.HasSuffix(mode, "listing-failed")}, nil
				}}
			m.refreshRemoteTree(t.Context(), record, nil, &anchorTreeRefresh{})
			rev, err := anchorMergeBoundRevision(record)
			if mode == "head-offline" || mode == "zero-head-offline" {
				if err != nil || rev != baseline {
					t.Fatal("seed lost behind unavailable HEAD", rev, err)
				}
			} else if err == nil {
				t.Fatal("invalid/missing baseline admitted", rev)
			}
			if mode == "corrupt-tree" {
				if raw, err := os.ReadFile(anchorTreePath(point)); err != nil || string(raw) != "broken" {
					t.Fatal("corrupt manifest silently overwritten", err)
				}
			}
			if raw, err := os.ReadFile(local); err != nil || string(raw) != "unpublished local edit" {
				t.Fatal("local edit changed", err)
			}
		})
	}
}
