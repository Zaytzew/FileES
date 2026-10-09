//go:build !nocfapi

package main

import (
	"context"
	"filees/pkg/client"
	"io"
	"slices"
	"testing"
	"time"
)

func TestAnchorDirectoryBridgeFramingAndExactACK(t *testing.T) {
	bridge := &anchorBridge{}
	helper, _ := runAnchorBridge(t, bridge)
	_, _ = io.WriteString(helper.toDaemon, "{\"ok\":true}\n")
	node := anchorRemoteNode{client.HistoryTreeNode{Path: "a/child", Kind: "file"}, "a/child"}
	parent := cfapiAnswer{OK: true, Directory: true, USN: "7", Identity: "a"}
	states := map[string]cfapiAnswer{"a/child": {OK: true, Placeholder: true, Partial: true, InSync: true, USN: "8", Identity: "a/child"}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- bridge.setDirectoryState(ctx, "a", parent, true, []anchorRemoteNode{node}, states) }()
	fields := helper.answer(t)
	if len(fields) != 7 || fields[0] != "directory-set" || fields[2] != "a" || fields[6] != "1" {
		t.Fatal("invalid framed header", fields)
	}
	child := helper.answer(t)
	if !slices.Equal(child, []string{"child", "8", "a/child"}) {
		t.Fatal("invalid descendant frame", child)
	}
	_, _ = io.WriteString(helper.toDaemon, "state-set\t"+fields[1]+"\t"+hresultOK+"\n")
	select {
	case err := <-done:
		t.Fatal("wrong ACK authorized directory", err)
	case <-time.After(20 * time.Millisecond):
	}
	_, _ = io.WriteString(helper.toDaemon, "directory-set\t"+fields[1]+"\t"+hresultOK+"\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	states["a/child"] = cfapiAnswer{OK: true, Placeholder: true, Partial: true, InSync: true, USN: "8", Identity: "bad\nstate-set"}
	if err := (&anchorBridge{}).setDirectoryState(t.Context(), "a", parent, true, []anchorRemoteNode{node}, states); err == nil {
		t.Fatal("control characters admitted")
	}
}

func TestAnchorDirectoryOrderAndBoundedSelection(t *testing.T) {
	nodes := []anchorRemoteNode{
		{client.HistoryTreeNode{Path: "a", Kind: "dir"}, "a"},
		{client.HistoryTreeNode{Path: "a/b", Kind: "dir"}, "a/b"},
		{client.HistoryTreeNode{Path: "a/b/c.bin", Kind: "file", Size: 1}, "a/b/c.bin"},
		{client.HistoryTreeNode{Path: "ab", Kind: "dir"}, "ab"},
	}
	order, err := anchorDirectoryOrder(nodes)
	if err != nil || !slices.Equal(order, []string{"a/b", "a", "ab", "."}) {
		t.Fatal(order, err)
	}
	selected, err := anchorDirectorySelection(nodes, "a")
	if err != nil || len(selected) != 2 || selected[0].Path != "a/b" {
		t.Fatal(selected, err)
	}
	if _, err := anchorDirectorySelection(nodes, "../a"); err == nil {
		t.Fatal("escaped selection")
	}
	large := make([]anchorRemoteNode, anchorDirectoryObjectLimit+1)
	if _, err := anchorDirectorySelection(large, "."); err == nil {
		t.Fatal("unbounded proof")
	}
}

func TestAnchorDirectorySVNConfirmationNeverInfersFromAbsence(t *testing.T) {
	states := map[string]cfapiAnswer{
		"a":        {OK: true, Directory: true, Placeholder: true, InSync: true, USN: "7", Identity: "a"},
		"a/f":      {OK: true, Placeholder: true, Local: true, InSync: true, USN: "8", Identity: "filees.local/v1"},
		"a/remote": {OK: true, Placeholder: true, Partial: true, InSync: true, USN: "9", Identity: "a/remote"},
	}
	clean := []client.StatusEntry{{Path: "a", Item: "normal", Props: "none"}, {Path: "a/f", Item: "normal", Props: "normal"}, {Path: "a/remote", Item: "unversioned", Props: "none"}}
	if !anchorDirectorySVNClean("root", "a", clean, states) {
		t.Fatal("valid mixed metadata refused")
	}
	for _, change := range []client.StatusEntry{
		{Path: "a/f", Item: "modified", Props: "none"},
		{Path: "a/f", Item: "missing", Props: "none"},
		{Path: "a/f", Item: "none", Props: "none"},
		{Path: "a", Item: "normal", Props: "modified"},
		{Path: "a", Item: "normal", Props: "none", Conflicted: true},
		{Path: "a/unexpected", Item: "unversioned", Props: "none"},
		{Path: "ab", Item: "normal", Props: "none"},
	} {
		rows := append(slices.Clone(clean), change)
		if anchorDirectorySVNClean("root", "a", rows, states) {
			t.Fatal("false directory confirmation", change)
		}
	}
	if anchorDirectorySVNClean("root", "a", nil, states) {
		t.Fatal("missing parent row accepted")
	}
}

func TestAnchorDirectoryStateMatchesExactIdentityAndKind(t *testing.T) {
	node := anchorRemoteNode{client.HistoryTreeNode{Path: "a/f", Kind: "file", Size: 12}, "a/f"}
	state := cfapiAnswer{OK: true, Placeholder: true, Partial: true, InSync: true, Identity: "a/f", USN: "7"}
	if !anchorDirectoryStateMatches(node, state) {
		t.Fatal("byte-free remote proof refused")
	}
	for _, change := range []func(*cfapiAnswer){
		func(s *cfapiAnswer) { s.Identity = "other" }, func(s *cfapiAnswer) { s.OK = false },
		func(s *cfapiAnswer) { s.Placeholder = false }, func(s *cfapiAnswer) { s.Directory = true },
		func(s *cfapiAnswer) { s.Modified = 1 }, func(s *cfapiAnswer) { s.USN = "0" },
		func(s *cfapiAnswer) { s.Local = true },
	} {
		s := state
		change(&s)
		if anchorDirectoryStateMatches(node, s) {
			t.Fatal("invalid descendant proof", s)
		}
	}
}
