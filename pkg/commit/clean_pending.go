package commit

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"filees/pkg/watcher"
)

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
