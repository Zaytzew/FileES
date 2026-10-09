//go:build !nocfapi

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"filees/pkg/client"
	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
)

// Only bounded metadata is retained. No file bytes or filesystem recursion are
// used to plan an action: candidates come from an authorised SVN manifest and
// the helper checks their actual sync-root membership by handle.
const anchorBatchFileLimit = 20000
const anchorBatchPathBytes = 8 << 20

type anchorBatchFile struct {
	rel   string
	state cfapiAnswer
}

type anchorBatchSelection struct {
	preview  contract.RepoAnchorPlan
	files    []anchorBatchFile
	revision int64
}

func anchorBatchPlan(ctx context.Context, manifest, selection, action, binding string,
	probe func([]string) ([]cfapiAnswer, error)) (anchorBatchSelection, error) {
	var result anchorBatchSelection
	if (action != "download" && action != "release") ||
		(selection != "." && !anchorBatchSafePath(selection)) {
		return result, errors.New("invalid folder action")
	}
	var paths []string
	sizes := make(map[string]int64)
	pathBytes := 0
	found := selection == "."
	result.preview.Directory = found
	seen := make(map[string]bool)
	consent := sha256.New()
	consentEncoder := json.NewEncoder(consent)
	_ = consentEncoder.Encode([]string{binding, selection, action})
	err := client.EachHistoryTreeNode(manifest, func(node client.HistoryTreeNode) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if selection != "." && node.Path != selection && !strings.HasPrefix(node.Path, selection+"/") {
			return nil
		}
		if !anchorBatchSafePath(node.Path) {
			return errors.New("unsafe folder manifest")
		}
		folded := strings.ToLower(node.Path)
		if seen[folded] {
			return errors.New("ambiguous folder manifest")
		}
		seen[folded] = true
		_ = consentEncoder.Encode(node)
		if len(seen) > anchorBatchFileLimit || pathBytes+len(node.Path) > anchorBatchPathBytes {
			return errors.New("folder plan too large; choose a smaller subtree")
		}
		pathBytes += len(node.Path)
		if node.Path == selection {
			found = true
			result.preview.Directory = node.Kind == "dir"
		}
		if node.Kind == "file" {
			if node.Size < 0 {
				return errors.New("invalid repository file size")
			}
			paths = append(paths, node.Path)
			sizes[node.Path] = node.Size
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	if !found {
		return result, errors.New("selected path is absent from the repository tree")
	}
	hash := sha256.New()
	encode := json.NewEncoder(hash)
	_ = encode.Encode([]string{binding, selection, action})
	for start := 0; start < len(paths); start += 64 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		batch := paths[start:min(start+64, len(paths))]
		states, err := probe(batch)
		if err != nil {
			return result, err
		}
		if len(states) != len(batch) {
			return result, errors.New("incomplete folder metadata")
		}
		for i, state := range states {
			// Absence is not a busy writer or permission to restore a user's
			// deletion. Refuse the whole selection before any hydration.
			if !state.OK && (strings.EqualFold(state.HResult, "0x80070002") || strings.EqualFold(state.HResult, "0x80070003")) {
				return result, fmt.Errorf("%w: %q (hresult=%s)", ipcserver.ErrAnchorPathMissing, batch[i], state.HResult)
			}
			usn, usnErr := strconv.ParseInt(state.USN, 10, 64)
			if !state.OK || state.Size == nil || *state.Size < 0 || usnErr != nil || usn <= 0 {
				return result, fmt.Errorf("unavailable folder metadata: ok=%v size=%v usn=%q hresult=%s", state.OK, state.Size != nil, state.USN, state.HResult)
			}
			_ = encode.Encode(struct {
				Path  string
				State cfapiAnswer
			}{batch[i], state})
			if state.Partial {
				if !state.Placeholder || state.Local || !anchorRemoteMatches(state.Identity, batch[i]) || *state.Size != sizes[batch[i]] {
					return result, errors.New("unrecognised remote placeholder")
				}
				if action == "release" {
					continue
				}
			} else {
				if action == "download" {
					if state.Placeholder && !state.Local {
						return result, commit.ErrLocalStatusBusy
					}
					continue
				}
				if !state.Placeholder || !state.Local || !state.InSync {
					return result, errors.New("folder contains local work that cannot be released")
				}
			}
			if result.preview.Bytes > math.MaxInt64-*state.Size {
				return result, errors.New("folder size overflow")
			}
			result.files = append(result.files, anchorBatchFile{batch[i], state})
			result.preview.Files++
			result.preview.Bytes += *state.Size
		}
	}
	result.preview.Fingerprint = hex.EncodeToString(hash.Sum(nil))
	if action == "download" {
		result.preview.SelectionFingerprint = hex.EncodeToString(consent.Sum(nil))
	}
	return result, nil
}

func anchorBatchSafePath(rel string) bool {
	clean, ok := cleanAnchorIdentity(rel)
	if !ok || clean != rel || strings.ContainsAny(rel, "\\:\r") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
	}
	return true
}

// Each item is independently protected. A failure after an earlier success is
// a partial result, not a rollback and not permission to skip the refused item.
func runAnchorBatch(ctx context.Context, selection anchorBatchSelection,
	apply func(context.Context, anchorBatchFile) error) (completed int64, err error) {
	for _, file := range selection.files {
		if err := ctx.Err(); err != nil {
			return completed, err
		}
		if err := apply(ctx, file); err != nil {
			return completed, fmt.Errorf("folder action stopped after %d of %d files: %w", completed, len(selection.files), err)
		}
		completed++
	}
	return completed, nil
}
