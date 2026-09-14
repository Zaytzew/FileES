package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"filees/pkg/activity"
	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"filees/pkg/watcher"
)

// repairOrphanPendingActivity repairs presentation rows left by an older
// daemon after their durable staging entry was already retired. It never
// changes staging or SVN: a successful status check must prove that the path
// is clean (normal when present, absent/none when gone). Without the original
// receipt it says reconciled, not published, and invents no revision.
func (s *Service) repairOrphanPendingActivity(ctx context.Context, wc string) {
	lister, ok := s.Activity.(interface{ List() []activity.Entry })
	if !ok {
		return
	}
	s.mu.Lock()
	staged := make(map[string]bool, len(s.staging))
	for rel := range s.staging {
		staged[rel] = true
	}
	s.mu.Unlock()
	entriesByPath := make(map[string]activity.Entry)
	var paths []string
	for _, entry := range lister.List() {
		if entry.RepoID == s.repoID && entry.Stage == activity.Pending && !staged[entry.Path] && safeIntentPath(entry.Path) {
			entriesByPath[entry.Path] = entry
			paths = append(paths, entry.Path)
		}
	}
	if len(paths) == 0 {
		return
	}
	status, err := s.Cli.Status(ctx, wc, paths)
	if err != nil {
		return
	}
	states := make(map[string]client.StatusEntry, len(status))
	for _, item := range status {
		states[strings.ReplaceAll(item.Path, "\\", "/")] = item
	}
	changed := false
	for _, rel := range paths {
		item, reported := states[rel]
		info, statErr := os.Lstat(filepath.Join(wc, filepath.FromSlash(rel)))
		clean := statErr == nil && (info.Mode().IsRegular() || info.IsDir()) && reported && item.Item == "normal" && (item.Props == "none" || item.Props == "normal")
		gone := errors.Is(statErr, os.ErrNotExist) && (!reported || item.Item == "none")
		if !clean && !gone {
			continue
		}
		entry := entriesByPath[rel]
		entry.Stage, entry.Revision, entry.ErrorID = activity.Reconciled, 0, ""
		if err := s.Activity.Record(entry); err != nil {
			continue
		}
		changed = true
	}
	if changed {
		s.emit(contract.EvActivityChanged, nil)
	}
}

// reconcileCleanPending requires wcOpMu. Only explicit normal text AND props
// on an unchanged, existing regular file prove a watcher add/modify is a no-op.
// Missing, deletions, moves, directories and unknown status stay pending: this
// is not a portable-name gate or a licence to discard unrepresentable paths.
func (s *Service) reconcileCleanPending(ctx context.Context, wc string) {
	if s.intentPending(wc) {
		return
	}
	type candidate struct {
		item    *stageItem
		version uint64
		info    os.FileInfo
	}
	candidates := make(map[string]candidate)
	var paths []string
	s.mu.Lock()
	for rel, it := range s.staging {
		if it.IsDir || it.OldRel != "" || it.MoveScheduled || (it.Op != watcher.Added && it.Op != watcher.Modified) {
			continue
		}
		info, err := os.Lstat(filepath.Join(wc, filepath.FromSlash(rel)))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		candidates[rel] = candidate{it, it.ver, info}
		paths = append(paths, rel)
	}
	s.mu.Unlock()
	if len(paths) == 0 {
		return
	}
	entries, err := s.Cli.Status(ctx, wc, paths)
	if err != nil {
		return
	} // Failure/absence is never proof of a clean path.
	changed := false
	for _, entry := range entries {
		rel := strings.ReplaceAll(entry.Path, "\\", "/")
		c, ok := candidates[rel]
		if !ok || entry.Item != "normal" || (entry.Props != "none" && entry.Props != "normal") {
			continue
		}
		info, err := os.Lstat(filepath.Join(wc, filepath.FromSlash(rel)))
		if err != nil || !info.Mode().IsRegular() || !os.SameFile(c.info, info) || c.info.Size() != info.Size() || !c.info.ModTime().Equal(info.ModTime()) {
			continue
		}
		s.mu.Lock()
		current := s.staging[rel]
		if current == c.item && current.ver == c.version {
			delete(s.staging, rel)
			changed = true
		}
		s.mu.Unlock()
		if current == c.item && current.ver == c.version {
			s.recordReconciled(rel, c.item.Op)
		}
	}
	if changed {
		s.saveCache()
	}
}
