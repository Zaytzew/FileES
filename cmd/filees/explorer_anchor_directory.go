//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"filees/pkg/client"
)

const anchorDirectoryObjectLimit = 4096

// This is synchronization metadata, NOT a pin/dehydration request. Native
// hydration badges remain the Shell's projection of real descendant content.
func anchorDirectoryOrder(nodes []anchorRemoteNode) ([]string, error) {
	if err := validateAnchorRemoteNodes(nodes); err != nil {
		return nil, err
	}
	dirs := []string{"."}
	for _, n := range nodes {
		if n.Kind == "dir" {
			dirs = append(dirs, n.Path)
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i] == "." {
			return false
		}
		if dirs[j] == "." {
			return true
		}
		a, b := strings.Count(dirs[i], "/"), strings.Count(dirs[j], "/")
		if a != b {
			return a > b
		}
		return dirs[i] < dirs[j]
	})
	return dirs, nil
}

func anchorDirectorySelection(nodes []anchorRemoteNode, dir string) ([]anchorRemoteNode, error) {
	if dir != "." && !anchorBatchSafePath(dir) {
		return nil, errors.New("invalid directory selection")
	}
	var selected []anchorRemoteNode
	for _, n := range nodes {
		if dir == "." || strings.HasPrefix(n.Path, dir+"/") {
			selected = append(selected, n)
		}
		if len(selected) > anchorDirectoryObjectLimit {
			return nil, errors.New("directory observation exceeds bounded object budget")
		}
	}
	return selected, nil
}

func anchorDirectoryStateMatches(n anchorRemoteNode, s cfapiAnswer) bool {
	if !s.OK || !s.Placeholder || s.Directory != (n.Kind == "dir") || s.Modified != 0 {
		return false
	}
	if usn, err := strconv.ParseInt(s.USN, 10, 64); err != nil || usn <= 0 {
		return false
	}
	if s.Local {
		return n.Kind == "file" && !s.Partial && s.Identity == "filees.local/v1"
	}
	return s.Identity == n.Identity
}

// No missing status row is publication proof. Remote-only nodes can lack WC
// metadata, but they must have exact, clean manifest-bound CFAPI metadata.
func anchorDirectorySVNClean(root, dir string, rows []client.StatusEntry, states map[string]cfapiAnswer) bool {
	seen := false
	for _, row := range rows {
		rel := filepath.ToSlash(row.Path)
		if filepath.IsAbs(row.Path) {
			p, err := filepath.Rel(root, row.Path)
			if err != nil {
				return false
			}
			rel = filepath.ToSlash(p)
		}
		rel = path.Clean(rel)
		if rel == ".svn" || strings.HasPrefix(rel, ".svn/") || rel == ".filees" || strings.HasPrefix(rel, ".filees/") {
			continue
		}
		if dir != "." && rel != dir && !strings.HasPrefix(rel, dir+"/") {
			return false
		}
		s, ok := states[rel]
		if !ok || row.Conflicted || (row.Props != "none" && row.Props != "normal") {
			return false
		}
		if rel == dir {
			seen = true
		}
		if row.Item == "normal" {
			continue
		}
		if s.Placeholder && !s.Local && s.InSync && (row.Item == "none" || row.Item == "unversioned" || row.Item == "excluded") {
			continue
		}
		return false
	}
	return seen
}

func (bridge *anchorBridge) setDirectoryState(ctx context.Context, dir string, parent cfapiAnswer, clean bool, nodes []anchorRemoteNode, states map[string]cfapiAnswer) error {
	if dir != "." && !anchorBatchSafePath(dir) {
		return errors.New("invalid directory path")
	}
	token, err := strconv.ParseInt(parent.USN, 10, 64)
	if err != nil || token <= 0 || !parent.OK || !parent.Directory || strings.ContainsAny(parent.Identity, "\t\r\n\x00") {
		return errors.New("invalid directory observation")
	}
	flag := "0"
	var lines []string
	if clean {
		flag = "1"
		if len(nodes) > anchorDirectoryObjectLimit {
			return errors.New("directory observation too large")
		}
		for _, n := range nodes {
			s := states[n.Path]
			if !anchorDirectoryStateMatches(n, s) || !s.InSync {
				return errors.New("directory has unconfirmed descendants")
			}
			rel := n.Path
			if dir != "." {
				if !strings.HasPrefix(rel, dir+"/") {
					return errors.New("directory descendant outside selection")
				}
				rel = strings.TrimPrefix(rel, dir+"/")
			}
			line := rel + "\t" + s.USN + "\t" + s.Identity
			if len(line)+2 >= 4096 || strings.ContainsAny(s.Identity, "\t\r\n\x00") {
				return errors.New("invalid descendant observation")
			}
			lines = append(lines, line)
		}
	}
	payload := strconv.Itoa(len(lines))
	if len(lines) > 0 {
		payload += "\n" + strings.Join(lines, "\n")
	}
	// One queued write contains the entire framed packet. It cannot interleave
	// with a fetch reply. Cancellation reaps the provider before releasing WC.
	return bridge.connectedCommand(ctx, "directory-set", "directory-set", dir, parent.USN, parent.Identity, flag, payload)
}
