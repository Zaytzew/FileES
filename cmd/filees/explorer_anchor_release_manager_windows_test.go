//go:build windows && !nocfapi

package main

import (
	"errors"
	"path/filepath"
	"testing"

	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

func TestAnchorReleaseManagerRequiresFreshPointAndConnectedProvider(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "local.json"))
	if err != nil {
		t.Fatal(err)
	}
	point, err := store.BeginAnchorAttach("server", "repo", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveAttach(point.OperationID, "server", "repo", "svn+ssh://_filees-client@example/repo", "rw"); err != nil {
		t.Fatal(err)
	}
	point, err = store.MarkAttached(point.OperationID, "repo")
	if err != nil {
		t.Fatal(err)
	}
	server := ipcserver.New("unused")
	server.RegisterRepoAccess("repo", point.RepoURL, point.LocalPath, "server", "rw")
	m := &anchorManager{lifecycle: store, repos: server.RepoState}
	if err := m.currentReleaseRecord(point); err != nil {
		t.Fatal(err)
	}
	// No provider is not permission to invoke ordinary SVN.
	if err := m.release(t.Context(), "server", "repo", "file.txt"); !errors.Is(err, ipcserver.ErrDepthUnavailable) {
		t.Fatal(err)
	}
	for _, field := range []string{"url", "wc", "operation", "detaching"} {
		stale := point
		switch field {
		case "url":
			stale.RepoURL += "-other"
		case "wc":
			stale.LocalPath += "-other"
		case "operation":
			stale.OperationID = "other"
		case "detaching":
			if _, err := store.BeginDetach("server", "repo", false); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.currentReleaseRecord(stale); !errors.Is(err, ipcserver.ErrDepthUnavailable) {
			t.Fatal(field, err)
		}
	}
}
