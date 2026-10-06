//go:build !nocfapi

package main

import (
	"filees/pkg/client"
	"filees/pkg/localrepo"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteOpenUsesEntryRevisionAndLegacySeed(t *testing.T) {
	probe := &anchorPinnedFileProbe{size: 7}
	m := &anchorManager{newSVN: func(string) (client.Client, error) { return probe, nil }}
	record := localrepo.Record{ServerID: "test", RepoURL: "svn://fixture/repo", LocalPath: t.TempDir()}
	id, _ := anchorRemoteIdentity("file.txt", 17, 7)
	dest, rev, err := m.materialize(t.Context(), record, id)
	if err != nil || rev != 17 || probe.revision != 17 || probe.headReads != 0 {
		t.Fatal("entry revision not pinned", rev, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(dest)) })
	if _, _, err := m.materializeAt(t.Context(), record, id, 18, 7); err == nil {
		t.Fatal("explicit request overrode identity revision")
	}
	if _, _, err := m.materialize(t.Context(), record, "file.txt"); err == nil {
		t.Fatal("legacy entry fetched HEAD without seed")
	}
}

func TestAnchorRemoteIdentityPinsAndValidates(t *testing.T) {
	id, err := anchorRemoteIdentity("folder/document.dwg", 17, 4096)
	if err != nil {
		t.Fatal(err)
	}
	rel, rev, size, ok := parseAnchorRemoteIdentity(id)
	if !ok || rel != "folder/document.dwg" || rev != 17 || size != 4096 {
		t.Fatal("bad round trip")
	}
	for _, bad := range []string{"filees.remote/v1:017:4096:a", "filees.remote/v1:17:-1:a", "filees.remote/v1:17:1:../a", "filees.remote/v1:17:1:a:b", "filees.remote/v1:0:1:a"} {
		if _, _, _, ok := parseAnchorRemoteIdentity(bad); ok {
			t.Fatalf("accepted %s", bad)
		}
	}
	if anchorExplicitKey("C:/program.exe", id) != anchorExplicitKey("C:/program.exe", rel) {
		t.Fatal("explicit intent lost")
	}
}

func TestAnchorRemoteTreeRejectsAmbiguityAndMissingParents(t *testing.T) {
	node := func(rel, kind string) anchorRemoteNode {
		return anchorRemoteNode{client.HistoryTreeNode{Path: rel, Kind: kind, Size: 1}, rel}
	}
	for _, nodes := range [][]anchorRemoteNode{{node("a", "file"), node("A", "file")}, {node("a/b", "file")}, {node("a", "file"), node("a/b", "file")}, {node("../a", "file")}} {
		if validateAnchorRemoteNodes(nodes) == nil {
			t.Fatal("unsafe tree accepted")
		}
	}
	if err := validateAnchorRemoteNodes([]anchorRemoteNode{node("a", "dir"), node("a/b", "dir"), node("a/b/c", "file")}); err != nil {
		t.Fatal(err)
	}
}
