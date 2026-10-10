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
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

// A bounded metadata observer, not a second synchronizer. It reads
// local availability/USN and SVN status; the connected helper compares the
// token before updating local metadata. A complete pass also schedules the
// bounded remote listing; neither pass hydrates bytes or publishes changes.
// Dirty files are never promoted on the absence of a status row or an error.
func (m *anchorManager) observeLocalStates(ctx context.Context, record localrepo.Record, bridge *anchorBridge) {
	period := m.stateEvery
	if period <= 0 {
		period = 3 * time.Second
	}
	cursor := ""
	remote := &anchorTreeRefresh{}
	directories := &anchorDirectoryCursor{}
	for ctx.Err() == nil {
		more := anchorLocalStateTurn(ctx, &cursor, func(ctx context.Context) error {
			return m.refreshLocalStates(ctx, record, bridge, &cursor)
		})
		if !more {
			m.refreshRemoteTree(ctx, record, bridge, remote)
			_ = m.refreshDirectoryStates(ctx, record, bridge, directories)
		}
		delay := period
		if more {
			delay = min(period, 250*time.Millisecond)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// One turn starts at most eight batches within a 500 ms soft budget, releasing
// the WC guard between them. Each batch retains its own two-second deadline:
// do not shorten a metadata ACK just because an earlier batch used the budget.
// An unfinished pass continues after a short yield;
// completion or failure returns to the normal idle/backoff interval.
// Roll back only the failed batch, so a busy writer does not cost a full pass.
func anchorLocalStateTurn(ctx context.Context, cursor *string, refresh func(context.Context) error) bool {
	until := time.Now().Add(500 * time.Millisecond)
	for range 8 {
		before := *cursor
		if err := ctx.Err(); err != nil {
			return false
		}
		batchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := refresh(batchCtx)
		cancel()
		if err != nil {
			*cursor = before
			return false
		}
		if *cursor == "" {
			return false
		}
		if !time.Now().Before(until) {
			return true
		}
	}
	return true
}

func (m *anchorManager) refreshLocalStates(ctx context.Context, record localrepo.Record, bridge *anchorBridge, cursor *string) error {
	leave, err := m.enterObserverIntent()
	if err != nil {
		return err // no walk/helper/ACK; retain the existing cursor
	}
	defer leave()
	candidates, paths, err := anchorLocalStateBatch(ctx, record.LocalPath, cursor, func(paths []string) ([]cfapiAnswer, error) {
		answer, err := m.call(ctx, strings.Join(paths, "\n")+"\n", "file-states", "--root", record.LocalPath)
		if err != nil || !answer.OK {
			return nil, errors.New("local metadata batch unavailable")
		}
		return answer.States, nil
	})
	if err != nil {
		return err
	}
	if ctx.Err() != nil || len(paths) == 0 {
		return ctx.Err()
	}
	return m.confirmLocalStates(ctx, record, bridge, candidates, paths)
}

// The limit applies even when every inspection fails. The cursor is a visited
// path in WalkDir order, not lexical order (directories sort differently).
func anchorLocalStateBatch(ctx context.Context, root string, cursor *string, probe func([]string) ([]cfapiAnswer, error)) (_ map[string]cfapiAnswer, _ []string, retErr error) {
	before := *cursor
	defer func() {
		if retErr != nil {
			*cursor = before // No complete metadata answer means no visited batch.
		}
	}()
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
		if cloudfiles.RecallsOnRead(info) {
			return nil
		}
		if !info.Mode().IsRegular() {
			// Windows may expose retained CFAPI files in subfolders as
			// ModeIrregular. Admit only the fully local FileES identity,
			// never generic reparse points, pipes or another provider.
			local := false
			if info.Mode()&fs.ModeIrregular != 0 {
				local, _ = cloudfiles.LocalState(abs)
			}
			if !local {
				return nil
			}
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

func (m *anchorManager) confirmLocalStates(ctx context.Context, record localrepo.Record, bridge *anchorBridge, candidates map[string]cfapiAnswer, paths []string) error {
	if m.repos == nil {
		return ipcserver.ErrDepthUnavailable
	}
	repo := m.repos(record.ServerID, record.RepoID)
	if repo == nil {
		return ipcserver.ErrDepthUnavailable
	}
	// The commit service keeps its WC operation lock from status until the
	// helper ACK (or its confirmed exit). USN guards bytes, not SVN properties.
	return repo.WithLocalStatus(ctx, paths, func(rows []client.StatusEntry) error {
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
