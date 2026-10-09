//go:build windows && !nocfapi

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
)

func TestAnchorDeleteCoordinatorProofIsExactBoundedAndOrdinary(t *testing.T) {
	for _, scenario := range []string{"valid", "missing-hash", "foreign-attachment", "foreign-revision", "trailing", "hardlink", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			s, cb, request, snapshot, inspect := testDeleteJournal(t)
			guards, root, err := guardAnchorMoveState(s.record)
			if err != nil {
				t.Fatal(err)
			}
			closeAnchorReleaseGuards(guards)
			tree := anchorRemoteTree{Schema: "filees.anchor-tree/v1", Operation: s.record.OperationID, URL: s.record.RepoURL, Root: root, Revision: request.BaseRevision,
				Nodes: []anchorRemoteNode{{HistoryTreeNode: client.HistoryTreeNode{Path: request.Source, Kind: "file", Size: 4}, Identity: cb.Identity}}}
			s.treeSHA256, err = deleteTreeDigest(tree)
			if err != nil {
				t.Fatal(err)
			}
			prepareDeleteJournal(t, s, cb, request, snapshot, inspect)
			intent := *s.in
			file := filepath.Join(anchorMoveStateDir(s.record), anchorDeleteTreeName)
			raw, err := json.Marshal(tree)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "missing-hash":
				intent.TreeSHA256 = ""
			case "foreign-attachment":
				tree.Operation = "another"
				raw, _ = json.Marshal(tree)
			case "foreign-revision":
				tree.Revision++
				raw, _ = json.Marshal(tree)
			case "trailing":
				raw = append(raw, []byte(" {}")...)
			}
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if scenario == "hardlink" {
				if err := os.Link(file, filepath.Join(anchorMoveStateDir(s.record), "proof-alias")); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			_, pin, err := readDeleteTree(ctx, s.record, intent)
			if pin != nil {
				pin.Close()
			}
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("scenario=%s err=%v", scenario, err)
			}
			if scenario != "valid" && pin != nil {
				t.Fatal("failed proof leaked its pin")
			}
		})
	}
}
