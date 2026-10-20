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
	"sync"

	"filees/pkg/anchorqueue"
	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/portablepath"
)

// Only bounded metadata is retained. No file bytes or filesystem recursion are
// used to plan an action: candidates come from an authorised SVN manifest and
// the helper checks their actual sync-root membership by handle.
const anchorBatchFileLimit = 20000
const anchorBatchPathBytes = 8 << 20

var errAnchorReleaseLocalWork = errors.New("folder contains local work awaiting publication")

type anchorBatchFile struct {
	rel   string
	state cfapiAnswer
}

type anchorBatchSelection struct {
	preview  contract.RepoAnchorPlan
	files    []anchorBatchFile
	revision int64
	rootID   string
}

// Bounded daemon-owned confirmations: the GUI submits only the returned digest,
// never a caller-supplied path list. Restart before enqueue requires a preview.
type anchorReleasePlan struct {
	payload              contract.RepoAnchorPlanPayload
	preview              contract.RepoAnchorPlan
	snapshot             anchorqueue.Snapshot
	operation, url, root string
}
type anchorReleasePlans struct {
	mu    sync.Mutex
	plans []anchorReleasePlan
}

func (c *anchorReleasePlans) put(p contract.RepoAnchorPlanPayload, s anchorBatchSelection, operation, url, root string) {
	x := anchorReleasePlan{payload: p, preview: s.preview, snapshot: anchorqueue.Snapshot{RootID: s.rootID}, operation: operation, url: url, root: root}
	for _, file := range s.files {
		x.snapshot.Paths = append(x.snapshot.Paths, file.rel)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.plans = append(c.plans, x)
	bytes := 0
	for i := len(c.plans) - 1; i >= 0; i-- {
		for _, path := range c.plans[i].snapshot.Paths {
			bytes += len(path)
		}
		if bytes > anchorBatchPathBytes || len(c.plans)-i > 32 {
			c.plans = append([]anchorReleasePlan(nil), c.plans[i+1:]...)
			break
		}
	}
}
func (c *anchorReleasePlans) get(p contract.RepoAnchorQueuePayload, operation, url, root string) (*anchorqueue.Snapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.plans) - 1; i >= 0; i-- {
		x := c.plans[i]
		if x.payload == p.RepoAnchorPlanPayload && x.preview.Fingerprint == p.Fingerprint && x.preview.Files == p.Files && x.operation == operation && x.url == url && x.root == root {
			copy := x.snapshot
			copy.Paths = append([]string(nil), copy.Paths...)
			return &copy, true
		}
	}
	return nil, false
}

func anchorBatchPlan(ctx context.Context, manifest, selection, action, binding string,
	probe func([]string) ([]cfapiAnswer, error)) (anchorBatchSelection, error) {
	return anchorBatchPlanSnapshot(ctx, manifest, selection, action, binding, nil, probe)
}

func anchorBatchPlanSnapshot(ctx context.Context, manifest, selection, action, binding string, snapshot []string,
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
	wanted := make(map[string]bool)
	for _, path := range snapshot {
		wanted[path] = true
	}
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
		folded := portablepath.CollisionKey(node.Path)
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
			if snapshot != nil && !wanted[node.Path] {
				return nil
			}
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
	if snapshot != nil && len(paths) != len(wanted) {
		return result, ipcserver.ErrAnchorSelectionChanged
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
			} else {
				if action == "download" {
					if state.Placeholder && !state.Local {
						// A prior provider may have delivered all bytes but lost
						// adoption. Include that explicit reconciliation work;
						// waiting for another FETCH_DATA would never finish it.
						rel, _, identitySize, valid := parseAnchorRemoteIdentity(state.Identity)
						if !valid || rel != batch[i] || (identitySize >= 0 && identitySize != *state.Size) || *state.Size != sizes[batch[i]] ||
							!anchorHydrationComplete(state, state.Identity, *state.Size) {
							return result, errors.New("unrecognised fully resident remote placeholder")
						}
					} else {
						continue
					}
				} else if !state.Placeholder || !state.Local || !state.InSync {
					return result, errAnchorReleaseLocalWork
				}
			}
			if result.preview.Bytes > math.MaxInt64-*state.Size {
				return result, errors.New("folder size overflow")
			}
			result.files = append(result.files, anchorBatchFile{batch[i], state})
			result.preview.Files++
			if action != "release" || !state.Partial {
				result.preview.Bytes += *state.Size
			}
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
