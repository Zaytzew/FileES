//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"strings"

	contract "filees/pkg/contract/v1"
)

// A directory's content state comes from every file in its known subtree.
// Local directory skeletons are not local content. The caller supplies the
// validated, attachment-bound manifest, never an arbitrary filesystem walk.
func anchorFolderAvailability(ctx context.Context, nodes []anchorRemoteNode, dir string,
	entries []contract.RepoHeadEntry, probe func([]string) ([]cfapiAnswer, error)) (map[string]contract.RepoHeadAvailability, error) {
	if err := validateAnchorRemoteNodes(nodes); err != nil {
		return nil, err
	}
	type totals struct {
		found, present, nestedDirs bool
		local, remote              int
	}
	folders := map[string]*totals{}
	for _, entry := range entries {
		if entry.Kind == "dir" && anchorBatchSafePath(entry.Name) && !strings.Contains(entry.Name, "/") {
			folders[entry.Name] = &totals{present: entry.Local}
		}
	}
	prefix := ""
	if dir != "" {
		if !anchorBatchSafePath(dir) {
			return nil, errors.New("invalid availability directory")
		}
		prefix = dir + "/"
	}
	var paths []string
	var owners []*totals
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(node.Path, prefix) {
			continue
		}
		rel := strings.TrimPrefix(node.Path, prefix)
		child, _, nested := strings.Cut(rel, "/")
		folder := folders[child]
		if folder == nil {
			continue
		}
		if !nested && node.Kind == "dir" {
			folder.found = true
		}
		if nested && node.Kind == "dir" {
			folder.nestedDirs = true
		}
		if nested && node.Kind == "file" {
			paths = append(paths, node.Path)
			owners = append(owners, folder)
		}
	}
	for start := 0; start < len(paths); start += 64 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+64, len(paths))
		states, err := probe(paths[start:end])
		if err != nil {
			return nil, err
		}
		if len(states) != end-start {
			return nil, errors.New("incomplete availability metadata")
		}
		for i, state := range states {
			if !state.OK || (state.Partial && (!state.Placeholder || !anchorRemoteMatches(state.Identity, paths[start+i]))) {
				return nil, errors.New("unavailable content metadata")
			}
			folder := owners[start+i]
			if state.Partial {
				folder.remote++
			} else {
				folder.local++
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := map[string]contract.RepoHeadAvailability{}
	for name, folder := range folders {
		if !folder.found {
			continue // A newer HEAD entry is not covered by the manifest yet.
		}
		switch {
		case folder.local > 0 && folder.remote > 0:
			result[name] = contract.HeadHybrid
		case folder.local > 0:
			result[name] = contract.HeadLocal
		case folder.remote > 0 || !folder.present:
			result[name] = contract.HeadRemote
		case folder.nestedDirs:
			// An empty directory hierarchy needs its own metadata proof.
			continue
		default:
			// A confirmed empty folder has no content left to download.
			result[name] = contract.HeadLocal
		}
	}
	return result, nil
}
