//go:build !nocfapi

package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"filees/pkg/client"
)

const anchorRemotePrefix = "filees.remote/v1:"

// The identity pins both revision and byte count. A fetch racing the next
// remote tree refresh still reads exactly the entry the application opened.
// Legacy seeds remain readable at their durable seed revision, never HEAD.
func anchorRemoteIdentity(rel string, revision, size int64) (string, error) {
	if !anchorBatchSafePath(rel) || revision <= 0 || size < 0 {
		return "", errors.New("invalid remote tree identity")
	}
	id := fmt.Sprintf("%s%d:%d:%s", anchorRemotePrefix, revision, size, rel)
	if len(utf16.Encode([]rune(id))) >= 512 {
		return "", errors.New("remote tree identity too long")
	}
	return id, nil
}

func parseAnchorRemoteIdentity(identity string) (rel string, revision, size int64, ok bool) {
	if !strings.HasPrefix(identity, anchorRemotePrefix) {
		rel, ok = cleanAnchorIdentity(identity)
		return rel, 0, -1, ok
	}
	parts := strings.SplitN(strings.TrimPrefix(identity, anchorRemotePrefix), ":", 3)
	if len(parts) != 3 {
		return "", 0, 0, false
	}
	revision, e1 := strconv.ParseInt(parts[0], 10, 64)
	size, e2 := strconv.ParseInt(parts[1], 10, 64)
	if e1 != nil || e2 != nil {
		return "", 0, 0, false
	}
	canonical, err := anchorRemoteIdentity(parts[2], revision, size)
	return parts[2], revision, size, err == nil && canonical == identity
}

func anchorRemoteMatches(identity, rel string) bool {
	actual, _, _, ok := parseAnchorRemoteIdentity(identity)
	return ok && actual == rel
}

type anchorRemoteNode struct {
	client.HistoryTreeNode
	Identity string `json:"identity"`
}

func validateAnchorRemoteNodes(nodes []anchorRemoteNode) error {
	seen := map[string]string{}
	bytes := 0
	for _, node := range nodes {
		bytes += len(node.Path) + len(node.Identity)
		if len(nodes) > anchorBatchFileLimit || bytes > 2*anchorBatchPathBytes ||
			!anchorBatchSafePath(node.Path) || (node.Kind != "dir" && node.Kind != "file") ||
			(node.Kind == "file" && node.Size < 0) || !anchorRemoteMatches(node.Identity, node.Path) {
			return errors.New("invalid or oversized remote tree")
		}
		fold := strings.ToLower(node.Path)
		if seen[fold] != "" {
			return errors.New("ambiguous remote tree")
		}
		seen[fold] = node.Kind
	}
	for _, node := range nodes {
		for parent, _, found := strings.Cut(node.Path, "/"); found; {
			if seen[strings.ToLower(parent)] != "dir" {
				return errors.New("remote tree parent missing")
			}
			rest := strings.TrimPrefix(node.Path, parent+"/")
			part, _, more := strings.Cut(rest, "/")
			parent += "/" + part
			found = more
		}
	}
	return nil
}
