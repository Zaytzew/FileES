//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/client"
	"filees/pkg/cloudfiles"
	"filees/pkg/localrepo"
)

// A bounded experimental observer, not a second synchronizer. It only reads
// local availability/USN and SVN status; the connected helper compares the
// token before updating metadata. No server read, hydration or publication.
// Dirty files are never promoted on the absence of a status row or an error.
func (m *anchorManager) observeLocalStates(ctx context.Context, record localrepo.Record, bridge *anchorBridge) {
	period := m.stateEvery
	if period <= 0 {
		period = 3 * time.Second
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	cursor := ""
	for ctx.Err() == nil {
		m.refreshLocalStates(ctx, record, bridge, &cursor)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *anchorManager) refreshLocalStates(ctx context.Context, record localrepo.Record, bridge *anchorBridge, cursor *string) {
	// One budget for enumeration, one metadata process, SVN and acknowledgements.
	// A busy writer is skipped, never queued behind a slow publication.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	candidates, paths, err := anchorLocalStateBatch(ctx, record.LocalPath, cursor, func(paths []string) ([]cfapiAnswer, error) {
		answer, err := m.call(ctx, strings.Join(paths, "\n")+"\n", "file-states", "--root", record.LocalPath)
		if err != nil || !answer.OK {
			return nil, errors.New("local metadata batch unavailable")
		}
		return answer.States, nil
	})
	if err != nil || len(paths) == 0 || ctx.Err() != nil {
		return
	}
	m.confirmLocalStates(ctx, record, bridge, candidates, paths)
}

// The limit applies even when every inspection fails. The cursor is a visited
// path in WalkDir order, not lexical order (directories sort differently).
func anchorLocalStateBatch(ctx context.Context, root string, cursor *string, probe func([]string) ([]cfapiAnswer, error)) (map[string]cfapiAnswer, []string, error) {
	const batchLimit = 64
	candidates := make(map[string]cfapiAnswer, batchLimit)
	paths := make([]string, 0, batchLimit)
	fullPass := true
	resume := *cursor == ""
	err := filepath.WalkDir(root, func(abs string, entry fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if entry.Name() == ".svn" || entry.Name() == ".filees" {
				return filepath.SkipDir
			}
			// Skip completed subtrees, but retain ancestors of the bookmark.
			// A file replaced by a directory must still expose its new children.
			if !resume && rel != "." && rel != *cursor && !strings.HasPrefix(*cursor, rel+"/") && !anchorWalkBefore(*cursor, rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if !resume {
			if !anchorWalkBefore(*cursor, rel) {
				return nil
			}
			resume = true // The bookmark itself may have been removed or renamed.
		}
		*cursor = rel
		if _, ok := cleanAnchorIdentity(rel); !ok {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if !info.Mode().IsRegular() || cloudfiles.RecallsOnRead(info) {
			return nil
		}
		paths = append(paths, rel)
		if len(paths) == batchLimit {
			fullPass = false
			return filepath.SkipAll
		}
		return nil
	})
	if fullPass && err == nil {
		*cursor = ""
	}
	if err != nil || len(paths) == 0 {
		return candidates, nil, err
	}
	states, err := probe(paths)
	if err != nil {
		return candidates, nil, err
	}
	if len(states) != len(paths) {
		return candidates, nil, errors.New("local metadata batch length mismatch")
	}
	eligible := paths[:0]
	for i, rel := range paths {
		state := states[i]
		if !state.OK || state.Partial || state.USN == "" {
			continue
		}
		candidates[rel] = state
		eligible = append(eligible, rel)
	}
	return candidates, eligible, nil
}

// WalkDir sorts each directory's entries, then descends immediately. Compare
// components, not whole strings: "a/child" precedes "a.txt" in that order.
// Cut avoids allocating one slice per path visited while finding the cursor.
func anchorWalkBefore(a, b string) bool {
	for {
		partA, restA, moreA := strings.Cut(a, "/")
		partB, restB, moreB := strings.Cut(b, "/")
		if partA != partB {
			return partA < partB
		}
		if !moreA || !moreB {
			return !moreA && moreB
		}
		a, b = restA, restB
	}
}

func (m *anchorManager) confirmLocalStates(ctx context.Context, record localrepo.Record, bridge *anchorBridge, candidates map[string]cfapiAnswer, paths []string) {
	if m.repos == nil {
		return
	}
	repo := m.repos(record.ServerID, record.RepoID)
	if repo == nil {
		return
	}
	// The commit service keeps its WC operation lock from status until the
	// helper ACK (or its confirmed exit). USN guards bytes, not SVN properties.
	_ = repo.WithLocalStatus(ctx, paths, func(rows []client.StatusEntry) error {
		for _, row := range rows {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			rel := filepath.ToSlash(row.Path)
			if filepath.IsAbs(row.Path) {
				local, err := filepath.Rel(record.LocalPath, row.Path)
				if err != nil {
					continue
				}
				rel = filepath.ToSlash(local)
			}
			state, found := candidates[rel]
			if !found {
				continue
			}
			clean, local := anchorSVNLocalState(row)
			if !local {
				continue
			} // An unadopted/new placeholder is not a local WC file.
			if state.Local && !clean && !state.InSync {
				continue // Do not rewrite unchanged pending metadata every cycle.
			}
			if state.Local && state.InSync {
				if clean {
					continue
				}
			}
			_ = bridge.setLocalState(ctx, rel, state.USN, clean)
		}
		return ctx.Err()
	})
}

func anchorSVNLocalState(row client.StatusEntry) (clean, local bool) {
	switch row.Item {
	case "normal", "modified", "replaced", "conflicted":
		local = true
	default:
		return false, false
	}
	props := strings.TrimSpace(row.Props)
	return row.Item == "normal" && !row.Conflicted && (props == "none" || props == "normal"), local
}
