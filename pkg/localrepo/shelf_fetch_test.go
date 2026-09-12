package localrepo

import (
	"github.com/google/uuid"
	"path/filepath"
	"strings"
	"testing"
)

func TestShelfFetchSurvivesRestartAndFencesConcurrentSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.BeginShelfAttach("office", uuid.NewString(), "svn+ssh://client@example/shelf", filepath.Join(t.TempDir(), "shelf"))
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.QueueShelfFetch(r.OperationID, "upload", "incoming/deep/file.txt", strings.Repeat("ab", 32), 12)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueShelfFetch(r.OperationID, "other", "other.txt", strings.Repeat("ab", 32), 12); err == nil {
		t.Fatal("overwrote pending fetch")
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(r.OperationID)
	if got.ShelfFetch != r.ShelfFetch {
		t.Fatal("lost receipt")
	}
	if _, err = s.SetShelfFetchState(r.OperationID, "wrong", "complete", nil); err == nil {
		t.Fatal("accepted stale completion")
	}
	if _, err = s.SetShelfFetchState(r.OperationID, r.ShelfFetch.ID, "complete", nil); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(r.OperationID)
	if got.ShelfFetch.State != "complete" {
		t.Fatal("lost completion")
	}
}

func TestShelfSelectionRejectsEscapesAndMetadata(t *testing.T) {
	for _, path := range []string{"", "../x", "/x", "a//b", "a/../b", `C:\x`, "x:stream", ".svn/wc.db", "a/.filees/x", "a. /x", "a/"} {
		if ValidShelfPath(path) {
			t.Errorf("accepted %q", path)
		}
	}
	if !ValidShelfPath("incoming/rysunek ą.dwg") {
		t.Fatal("rejected ordinary selection")
	}
}
