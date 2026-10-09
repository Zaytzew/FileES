//go:build windows && !nocfapi

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
	"golang.org/x/sys/windows"
)

func TestTreeMoveProofAndConsentBindObjectsParentsAndBaseline(t *testing.T) {
	r, move := testTreeMoveIntent(t)
	root := anchorReleaseFileID{Volume: 7, Index: 10}
	identity, err := anchorRemoteIdentity(move.Source, move.BaseRevision, 0)
	if err != nil {
		t.Fatal(err)
	}
	proof := anchorMoveObjects{Schema: "filees.anchor-move-objects/v1", Move: move,
		Tree: anchorRemoteTree{Schema: "filees.anchor-tree/v1", Operation: r.OperationID, URL: r.RepoURL,
			Root: root, Revision: move.BaseRevision, Nodes: []anchorRemoteNode{{HistoryTreeNode: client.HistoryTreeNode{Path: move.Source, Kind: "dir"}, Identity: identity}}},
		Objects: []anchorMoveObject{{Path: move.Source, ID: anchorReleaseFileID{Volume: 7, Index: 11}, Directory: true, RemoteIdentity: identity}}}
	for i, parent := range moveParentPaths(move) {
		id := anchorReleaseFileID{Volume: 7, Index: uint64(20 + i)}
		if parent == "." {
			id = root
		}
		proof.Parents = append(proof.Parents, anchorMoveObject{Path: parent, Directory: true, ID: id})
	}
	if err := validateMoveObjects(&proof, r, root, move); err != nil {
		t.Fatal(err)
	}
	base := testTreeMoveSnapshot(r, move)
	consent, err := treeMoveConsent(&proof, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"object", "parent", "attachment", "revision", "baseline", "scope"} {
		t.Run(mutation, func(t *testing.T) {
			raw, _ := json.Marshal(proof)
			var changed anchorMoveObjects
			if err := json.Unmarshal(raw, &changed); err != nil {
				t.Fatal(err)
			}
			snapshot := testTreeMoveSnapshot(r, move)
			switch mutation {
			case "object":
				changed.Objects[0].ID.Index++
			case "parent":
				changed.Parents[0].ID.Index++
			case "attachment":
				changed.Tree.Operation = "another attachment"
			case "revision":
				changed.Tree.Revision++
			case "baseline":
				snapshot.Entries[0].Size++
			case "scope":
				changed.Move.Destination = "another target"
			}
			got, err := treeMoveConsent(&changed, snapshot)
			if err != nil || got == consent {
				t.Fatal("stale consent accepted", err)
			}
		})
	}
	proof.Parents = proof.Parents[:len(proof.Parents)-1]
	if validateMoveObjects(&proof, r, root, move) == nil {
		t.Fatal("missing parent evidence accepted")
	}
}

func TestTreeMoveParentReplacementRefusesAndRetirementChecksOpenedRecord(t *testing.T) {
	r, _ := testTreeMoveIntent(t)
	parent := filepath.Join(r.LocalPath, "parent")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	guards, err := guardAnchorReleaseDirectories(parent, r.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(guards[len(guards)-1], &info); err != nil {
		t.Fatal(err)
	}
	closeAnchorReleaseGuards(guards)
	proof := &anchorMoveObjects{Parents: []anchorMoveObject{{Path: "parent", Directory: true, ID: anchorReleaseID(info)}}}
	if err := os.Rename(parent, parent+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if pins, err := pinMoveParents(r, proof); err == nil {
		closeAnchorReleaseGuards(pins)
		t.Fatal("replacement parent accepted")
	}
	file := filepath.Join(r.LocalPath, "phase.json")
	if err := os.WriteFile(file, []byte(`{"state":"other"}`), 0600); err != nil {
		t.Fatal(err)
	}
	refuse := errors.New("different phase record")
	if err := retireMoveFile(t.Context(), file, func(*os.File) error { return refuse }); !errors.Is(err, refuse) {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("refusal removed record", err)
	}
	if err := retireMoveFile(t.Context(), file, func(*os.File) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("approved record not retired", err)
	}
}
