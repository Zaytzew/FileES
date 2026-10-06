//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
)

func availabilityNode(t *testing.T, rel, kind string) anchorRemoteNode {
	t.Helper()
	id, err := anchorRemoteIdentity(rel, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	return anchorRemoteNode{client.HistoryTreeNode{Path: rel, Kind: kind}, id}
}

func TestAnchorFolderAvailabilityThreeContentStates(t *testing.T) {
	nodes := []anchorRemoteNode{
		availabilityNode(t, "folder", "dir"), availabilityNode(t, "folder/nested", "dir"),
		availabilityNode(t, "folder/nested/one", "file"), availabilityNode(t, "folder/two", "file"),
		availabilityNode(t, "folder-other", "dir"), availabilityNode(t, "folder-other/three", "file"),
		availabilityNode(t, "empty", "dir"),
	}
	entries := []contract.RepoHeadEntry{{Name: "folder", Kind: "dir", Local: true}, {Name: "empty", Kind: "dir", Local: true}, {Name: "newer", Kind: "dir", Local: true}}
	for _, tc := range []struct {
		name  string
		local int
		want  contract.RepoHeadAvailability
	}{{"remote", 0, contract.HeadRemote}, {"hybrid", 1, contract.HeadHybrid}, {"local", 2, contract.HeadLocal}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			result, err := anchorFolderAvailability(t.Context(), nodes, "", entries, func(paths []string) ([]cfapiAnswer, error) {
				calls++
				if len(paths) != 2 || paths[0] != "folder/nested/one" || paths[1] != "folder/two" {
					t.Fatal("wrong subtree", paths)
				}
				states := make([]cfapiAnswer, len(paths))
				for i, path := range paths {
					states[i] = cfapiAnswer{OK: true, Placeholder: true, Partial: i >= tc.local, Identity: path}
				}
				return states, nil
			})
			if err != nil || calls != 1 || result["folder"] != tc.want || result["empty"] != contract.HeadLocal || result["newer"] != "" {
				t.Fatal(result, calls, err)
			}
		})
	}
	// The same aggregation also works when browsing below the repository root.
	result, err := anchorFolderAvailability(t.Context(), nodes, "folder", []contract.RepoHeadEntry{{Name: "nested", Kind: "dir", Local: true}}, func(paths []string) ([]cfapiAnswer, error) {
		if len(paths) != 1 || paths[0] != "folder/nested/one" {
			t.Fatal(paths)
		}
		return []cfapiAnswer{{OK: true, Placeholder: true, Partial: true, Identity: paths[0]}}, nil
	})
	if err != nil || result["nested"] != contract.HeadRemote {
		t.Fatal(result, err)
	}
}

func TestAnchorFolderAvailabilityUnknownIsNotLocal(t *testing.T) {
	nodes := []anchorRemoteNode{availabilityNode(t, "dir", "dir"), availabilityNode(t, "dir/file", "file")}
	entries := []contract.RepoHeadEntry{{Name: "dir", Kind: "dir", Local: true}}
	for _, answers := range [][]cfapiAnswer{nil, {{OK: false}}, {{OK: true, Placeholder: true, Partial: true, Identity: "other/file"}}} {
		result, err := anchorFolderAvailability(t.Context(), nodes, "", entries, func([]string) ([]cfapiAnswer, error) { return answers, nil })
		if err == nil || result != nil {
			t.Fatal("unknown became a status", result, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := anchorFolderAvailability(ctx, nodes, "", entries, func([]string) ([]cfapiAnswer, error) { t.Fatal("probe after cancellation"); return nil, nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAnchorFolderAvailabilityBatchesMetadataAt64Paths(t *testing.T) {
	nodes := []anchorRemoteNode{availabilityNode(t, "dir", "dir")}
	for i := range 130 {
		nodes = append(nodes, availabilityNode(t, fmt.Sprintf("dir/file-%03d", i), "file"))
	}
	calls := 0
	result, err := anchorFolderAvailability(t.Context(), nodes, "", []contract.RepoHeadEntry{{Name: "dir", Kind: "dir", Local: true}}, func(paths []string) ([]cfapiAnswer, error) {
		calls++
		if len(paths) > 64 {
			t.Fatal("oversized metadata batch", len(paths))
		}
		states := make([]cfapiAnswer, len(paths))
		for i, path := range paths {
			states[i] = cfapiAnswer{OK: true, Placeholder: true, Partial: true, Identity: path}
		}
		return states, nil
	})
	if err != nil || calls != 3 || result["dir"] != contract.HeadRemote {
		t.Fatal(result, calls, err)
	}
}
