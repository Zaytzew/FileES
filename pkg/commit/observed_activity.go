package commit

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filees/pkg/activity"
	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"filees/pkg/watcher"
)

// cleanObservedPaths is local evidence, never a guess based on HEAD or a
// missing status row. Observe identity/size/mtime around SVN's text AND props
// check. Directories are checked at depth empty: child edits have their own
// events, and must not turn a received parent into an outgoing addition.
func (s *Service) cleanObservedPaths(ctx context.Context, wc string, paths []string) map[string]bool {
	clean := make(map[string]bool)
	if s.Cli == nil || wc == "" {
		return clean
	}
	before := make(map[string]os.FileInfo)
	var targets []string
	for _, rel := range paths {
		if _, ok := before[rel]; ok {
			continue
		}
		info, err := os.Lstat(filepath.Join(wc, filepath.FromSlash(rel)))
		if err == nil && (info.Mode().IsRegular() || info.IsDir()) {
			before[rel] = info
			targets = append(targets, rel)
		}
	}
	if len(targets) == 0 {
		return clean
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	entries, err := s.Cli.Status(ctx, wc, targets)
	if err != nil {
		return clean
	}
	for _, entry := range entries {
		rel := filepath.ToSlash(entry.Path)
		old := before[rel]
		if old == nil || entry.Item != "normal" || (entry.Props != "none" && entry.Props != "normal") {
			continue
		}
		now, err := os.Lstat(filepath.Join(wc, filepath.FromSlash(rel)))
		if err == nil && os.SameFile(old, now) && old.Mode() == now.Mode() && old.Size() == now.Size() && old.ModTime().Equal(now.ModTime()) {
			clean[rel] = true
		}
	}
	return clean
}

// A proven no-op must not erase a preceding receipt or claim a new commit.
func (s *Service) recordReconciled(rel string, op watcher.OpType) {
	if source, ok := s.Activity.(interface{ List() []activity.Entry }); ok {
		for _, entry := range source.List() {
			if entry.RepoID == s.repoID && entry.Path == rel && (entry.Stage == activity.Received || entry.Stage == activity.Published || entry.Stage == activity.Reconciled) {
				return
			}
		}
	}
	s.recordActivity(rel, op, activity.Reconciled, 0, "")
}

// RecordUpdate records successful incoming SVN notifications, with the local
// post-update revision (not a later remote HEAD). It is also used before Run
// for startup recovery. Time is receipt time,
// not the remote author's commit time. It never changes pending local edits.
// Callers serialize WC operations; startup calls occur before the scanner.
func (s *Service) RecordUpdate(ctx context.Context, repoID, wc, output string) {
	paths := updateActivityPaths(output)
	if len(paths) == 0 {
		return
	}
	rev, err := s.Cli.Revision(ctx, wc)
	if err != nil || rev <= 0 {
		return
	}
	if s.receivedDeletes == nil {
		s.receivedDeletes = make(map[string]bool)
	}
	ordered := make([]string, 0, len(paths))
	for rel := range paths {
		ordered = append(ordered, rel)
	}
	sort.Strings(ordered)
	for _, rel := range ordered {
		op := paths[rel]
		if op == watcher.Deleted {
			// Bound advisory receipts. Dropping one only falls back to the
			// conservative local queue; it never suppresses a user mutation.
			if len(s.receivedDeletes) >= 10000 {
				clear(s.receivedDeletes)
			}
			s.receivedDeletes[rel] = true
		} else {
			delete(s.receivedDeletes, rel)
		}
		if s.Activity == nil {
			continue
		}
		s.mu.Lock()
		pending := s.staging[rel] != nil
		s.mu.Unlock()
		if pending {
			// An incoming notification is not evidence that an already
			// queued local edit vanished. Reconciliation checks that later.
			continue
		}
		kind := activity.Modified
		if op == watcher.Added {
			kind = activity.Added
		}
		if op == watcher.Deleted {
			kind = activity.Deleted
		}
		var size *int64
		if op != watcher.Deleted {
			if info, err := os.Stat(filepath.Join(wc, filepath.FromSlash(rel))); err == nil && info.Mode().IsRegular() {
				n := info.Size()
				size = &n
			}
		}
		if err := s.Activity.Record(activity.Entry{RepoID: repoID, Path: rel, Kind: kind, Stage: activity.Received, Revision: rev, Size: size}); err != nil {
			s.Logger.Warnf("incoming activity: %v", err)
		}
	}
	s.emit(contract.EvActivityChanged, nil)
}

// SVN update's four notification columns are locale-independent. Accept only
// plain A/U/D: merges, conflicts, skipped paths and unrecognised output never
// prove an incoming replacement. Paths are relative to the requested WC;
// control lines, absolute paths and traversal are not receipts.
func updateActivityPaths(output string) map[string]watcher.OpType {
	paths := make(map[string]watcher.OpType)
	if changes, ok := client.UpdateChanges(output); ok {
		for path, action := range changes {
			op := watcher.Modified
			if action == "A" {
				op = watcher.Added
			}
			if action == "D" {
				op = watcher.Deleted
			}
			paths[path] = op
		}
		return paths
	}
	for _, line := range strings.Split(output, "\n") {
		if len(line) < 6 || line[1:5] != "    " {
			continue
		}
		op := watcher.Modified
		switch line[0] {
		case 'A':
			op = watcher.Added
		case 'U':
		case 'D':
			op = watcher.Deleted
		default:
			continue
		}
		rel := strings.TrimSuffix(line[5:], "\r")
		rel = strings.ReplaceAll(rel, "\\", "/")
		if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, ":") || strings.ContainsAny(rel, "\r\n\x00") {
			continue
		}
		rel = filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
		if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		paths[rel] = op
	}
	return paths
}

func (s *Service) receivedRemoval(ctx context.Context, rel string) bool {
	proven := s.receivedDeletes[rel]
	// Watcher debounce/replay may outlive the service. The durable incoming
	// receipt is evidence of origin, not an inference from an absent SVN row.
	if !proven {
		if source, ok := s.Activity.(interface{ List() []activity.Entry }); ok {
			for _, entry := range source.List() {
				if entry.RepoID == s.repoID && entry.Path == rel && entry.Stage == activity.Received && entry.Kind == activity.Deleted {
					proven = true
					break
				}
			}
		}
	}
	if !proven || s.Cli == nil {
		return false
	}
	s.mu.Lock()
	pending := s.staging[rel] != nil
	s.mu.Unlock()
	if pending {
		return false
	}
	_, err := os.Lstat(filepath.Join(s.wc, filepath.FromSlash(rel)))
	if !os.IsNotExist(err) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	entries, err := s.Cli.Status(ctx, s.wc, []string{rel})
	if err != nil {
		return false
	}
	for _, entry := range entries {
		// C explicitly emits none/none for an absent, no-longer-versioned
		// path; CLI may emit no rows. missing/deleted are LOCAL SVN changes.
		if filepath.ToSlash(entry.Path) != rel || (entry.Item != "unversioned" && entry.Item != "none") || (entry.Props != "none" && entry.Props != "normal") {
			return false
		}
	}
	_, err = os.Lstat(filepath.Join(s.wc, filepath.FromSlash(rel)))
	return os.IsNotExist(err)
}

// acceptEvents classifies before touching the outgoing queue or publishing a
// pending activity event. Failed/unknown checks retain the original events.
func (s *Service) acceptEvents(ctx context.Context, events []watcher.Event) {
	s.wcOpMu.Lock()
	defer s.wcOpMu.Unlock()
	filtered := events[:0]
	for _, ev := range events {
		if s.EventAcknowledged == nil || !s.EventAcknowledged(ev) {
			filtered = append(filtered, ev)
		}
	}
	events = filtered
	var paths []string
	for _, ev := range events {
		if ev.Op == watcher.Added || ev.Op == watcher.Modified || ev.Op == watcher.Renamed {
			paths = append(paths, ev.Rel)
		}
	}
	clean := s.cleanObservedPaths(ctx, s.wc, paths)
	if s.intentPending(s.wc) {
		clean = nil
	}
	for _, ev := range events {
		if s.refuseUnportable(ev) {
			continue
		}
		if (ev.Op == watcher.Added || ev.Op == watcher.Modified) && clean[ev.Rel] {
			s.mu.Lock()
			if it := s.staging[ev.Rel]; it != nil && it.OldRel == "" && !it.MoveScheduled && (it.Op == watcher.Added || it.Op == watcher.Modified) {
				delete(s.staging, ev.Rel)
			}
			s.mu.Unlock()
			s.recordReconciled(ev.Rel, ev.Op)
			continue
		}
		if ev.Op == watcher.Deleted && s.receivedRemoval(ctx, ev.Rel) {
			continue
		}
		if ev.Op == watcher.Renamed && clean[ev.Rel] && s.receivedRemoval(ctx, ev.OldRel) {
			continue
		}
		delete(s.receivedDeletes, ev.Rel)
		s.recordActivity(ev.Rel, ev.Op, activity.Detected, 0, "")
		s.addEventLocked(ev)
		s.recordActivity(ev.Rel, ev.Op, activity.Pending, 0, "")
	}
	s.saveCache()
}
