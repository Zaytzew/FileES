//go:build windows && !nocfapi

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/localrepo"
	"golang.org/x/sys/windows"
)

func TestAnchorMergeRevisionRequiresBoundManifestAndRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	r := localrepo.Record{LocalPath: root, OperationID: "attachment", RepoURL: "svn+ssh://host/repo"}
	guards, err := guardAnchorReleaseDirectories(root, root)
	if err != nil {
		t.Fatal(err)
	}
	var info windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(guards[len(guards)-1], &info)
	closeAnchorReleaseGuards(guards)
	if err != nil {
		t.Fatal(err)
	}
	tree := anchorRemoteTree{Schema: "filees.anchor-tree/v1", Operation: r.OperationID, URL: r.RepoURL,
		Root: anchorReleaseID(info), Revision: 7}
	if err := saveAnchorRemoteTree(r, tree); err != nil {
		t.Fatal(err)
	}
	if rev, err := anchorMergeBoundRevision(r); err != nil || rev != 7 {
		t.Fatal(rev, err)
	}
	tree.Revision = 0
	if err := saveAnchorRemoteTree(r, tree); err != nil {
		t.Fatal(err)
	}
	if rev, err := anchorMergeBoundRevision(r); err != nil || rev != 0 {
		t.Fatal("explicit empty r0 refused", rev, err)
	}
	raw, err := os.ReadFile(anchorTreePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(anchorTreePath(root), []byte(strings.Replace(string(raw), `"revision":0,`, "", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := anchorMergeBoundRevision(r); err == nil {
		t.Fatal("missing revision treated as r0")
	}
	if err := os.WriteFile(anchorTreePath(root), []byte(strings.Replace(string(raw), `"revision":0`, `"revision":null`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := anchorMergeBoundRevision(r); err == nil {
		t.Fatal("null revision treated as r0")
	}
	for _, baseline := range []int64{0, 7} {
		tree.Revision = baseline
		for _, mode := range []string{"attachment", "url", "root", "revision", "nonempty-zero"} {
			t.Run(mode, func(t *testing.T) {
				changed := tree
				switch mode {
				case "attachment":
					changed.Operation = "other"
				case "url":
					changed.URL += "-other"
				case "root":
					changed.Root.Index++
				case "revision":
					changed.Revision = -1
				case "nonempty-zero":
					changed.Revision = 0
					changed.Nodes = []anchorRemoteNode{{}}
				}
				if err := saveAnchorRemoteTree(r, changed); err != nil {
					t.Fatal(err)
				}
				if _, err := anchorMergeBoundRevision(r); err == nil {
					t.Fatal("unbound manifest admitted")
				}
			})
		}
	}
	if err := os.Remove(anchorTreePath(root)); err != nil {
		t.Fatal(err)
	}
	if _, err := anchorMergeBoundRevision(r); err == nil {
		t.Fatal("missing manifest admitted")
	}
}
