//go:build windows && !nocfapi

package main

import (
	"testing"

	"filees/pkg/client"
)

func TestAnchorReprojectionDoesNotHideRemoteRemoval(t *testing.T) {
	id, _ := anchorRemoteIdentity("folder", 1, 0)
	fileID, _ := anchorRemoteIdentity("folder/remote.bin", 1, 4)
	nodes := []anchorRemoteNode{{HistoryTreeNode: client.HistoryTreeNode{Path: "folder", Kind: "dir"}, Identity: id},
		{HistoryTreeNode: client.HistoryTreeNode{Path: "folder/remote.bin", Kind: "file", Size: 4}, Identity: fileID}}
	changes := anchorTreeChanges(anchorReprojectionBaseline(nodes, nil, nodes), nil)
	if len(changes) != 2 || !changes[0].remove || changes[0].node.Kind != "file" || !changes[1].remove || changes[1].node.Kind != "dir" {
		t.Fatal("hint hid a server-side removal", changes)
	}
	changes = anchorTreeChanges(anchorReprojectionBaseline(nodes, nodes, nodes), nodes)
	if len(changes) != 2 || changes[0].remove || changes[0].node.Kind != "dir" || changes[1].remove || changes[1].node.Kind != "file" {
		t.Fatal("hint did not force safe parent-first recreation", changes)
	}
}
