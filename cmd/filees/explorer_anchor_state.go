//go:build !nocfapi

package main

import (
	"context"
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
	candidates, paths, err := anchorLocalStateBatch(ctx, record.LocalPath, cursor, func(rel string) (cfapiAnswer, error) {
		return m.call(ctx, "", "file-state", "--root", record.LocalPath, "--rel", rel)
	})
	if err != nil || len(paths) == 0 || ctx.Err() != nil {
		return
	}
	m.confirmLocalStates(ctx, record, bridge, candidates, paths)
}

// The limit applies even when every inspection fails. The cursor is a visited
// path in WalkDir order, not lexical order (directories sort differently).
func anchorLocalStateBatch(ctx context.Context, root string, cursor *string, probe func(string) (cfapiAnswer, error)) (map[string]cfapiAnswer, []string, error) {
	const batchLimit = 64
	candidates := make(map[string]cfapiAnswer, batchLimit)
	paths := make([]string, 0, batchLimit)
	fullPass := true
	inspections := 0
	resume := *cursor == ""
	err := filepath.WalkDir(root, func(abs string, entry fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".svn" || entry.Name() == ".filees" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !resume {
			resume = rel == *cursor
			return nil
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
		// Count attempts, not successful candidates: unsupported metadata or
		// errors must not spawn one helper for every file in a large tree.
		inspections++
		if inspections == batchLimit {
			fullPass = false
		}
		// Content writes invalidate CFAPI's clean flag, property edits do not.
		// Keep checking SVN for clean local files, without spawning the helper.
		if local, inSync := cloudfiles.LocalState(abs); local && inSync {
			candidates[rel] = cfapiAnswer{Local: true, InSync: true}
			paths = append(paths, rel)
			if inspections == batchLimit {
				return filepath.SkipAll
			}
			return nil
		}
		state, err := probe(rel)
		if err != nil || !state.OK || state.Partial || state.USN == "" || (state.Local && state.InSync) {
			if inspections == batchLimit {
				return filepath.SkipAll
			}
			return nil
		}
		candidates[rel] = state
		paths = append(paths, rel)
		if inspections == batchLimit {
			fullPass = false
			return filepath.SkipAll
		}
		return nil
	})
	if fullPass {
		*cursor = ""
	}
	return candidates, paths, err
}

func (m *anchorManager) confirmLocalStates(ctx context.Context, record localrepo.Record, bridge *anchorBridge, candidates map[string]cfapiAnswer, paths []string) {
	svn, err := m.svn(record.ServerID)
	if err != nil {
		return
	}
	rows, err := svn.Status(ctx, record.LocalPath, paths)
	if err != nil {
		return
	}
	for _, row := range rows {
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
			// Only demoting clean here: a fresh token obtained after status
			// cannot be used to promote bytes that were not inspected by SVN.
			state, err = m.call(ctx, "", "file-state", "--root", record.LocalPath, "--rel", rel)
			if err != nil || !state.OK || state.Partial || state.USN == "" {
				continue
			}
		}
		_ = bridge.setLocalState(ctx, rel, state.USN, clean)
	}
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
