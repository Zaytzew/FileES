package commit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

type recoveryConflictFile struct {
	Path, SHA256 string
	Size         int64
	Item, Props  string
}

type recoveryConflict struct {
	Path  string
	Files []recoveryConflictFile
}

// Inspect only the narrow content-conflict case. Tree/property conflicts and
// mixed revisions require another workflow, not a best-effort destructive fix.
// The plan owns all paths; IPC supplies only the opaque plan id and choice.
func (s *Service) inspectRecoveryConflicts(ctx context.Context, head int64) ([]recoveryConflict, error) {
	entries, err := s.Cli.Status(ctx, s.wc, nil)
	if err != nil {
		return nil, err
	}
	var result []recoveryConflict
	status := make(map[string][2]string, len(entries))
	for _, entry := range entries {
		status[filepath.ToSlash(entry.Path)] = [2]string{entry.Item, entry.Props}
	}
	for _, entry := range entries {
		if !entry.Conflicted && entry.Item != "conflicted" && entry.Props != "conflicted" {
			continue
		}
		if entry.Item != "conflicted" || (entry.Props != "none" && entry.Props != "normal") {
			return nil, fmt.Errorf("unsupported tree/property conflict: %s", entry.Path)
		}
		rel := filepath.ToSlash(entry.Path)
		abs, err := intentSafePath(s.wc, cacheEntry{Rel: rel, Abs: filepath.Join(s.wc, filepath.FromSlash(rel))})
		if err != nil {
			return nil, err
		}
		revision, err := s.Cli.Revision(ctx, abs)
		if err != nil || revision != head {
			return nil, fmt.Errorf("conflict is not at inspected HEAD: %s", rel)
		}
		paths := []string{rel}
		// Keep the live working file AND .mine: the user may have edited again
		// since SVN saved .mine. Also preserve SVN's two base artifacts.
		siblings, err := os.ReadDir(filepath.Dir(abs))
		if err != nil {
			return nil, err
		}
		for _, sibling := range siblings {
			if !strings.HasPrefix(sibling.Name(), filepath.Base(abs)) {
				continue
			}
			suffix := strings.TrimPrefix(sibling.Name(), filepath.Base(abs))
			artifact := suffix == ".mine"
			if strings.HasPrefix(suffix, ".r") {
				_, parseErr := strconv.ParseUint(strings.TrimPrefix(suffix, ".r"), 10, 64)
				artifact = parseErr == nil
			}
			if artifact {
				paths = append(paths, filepath.ToSlash(filepath.Join(filepath.Dir(rel), sibling.Name())))
			}
		}
		sort.Strings(paths)
		conflict := recoveryConflict{Path: rel}
		for _, path := range paths {
			abs, err := intentSafePath(s.wc, cacheEntry{Rel: path, Abs: filepath.Join(s.wc, filepath.FromSlash(path))})
			if err != nil {
				return nil, err
			}
			size, hash, err := intentFileHash(ctx, abs)
			if err != nil {
				return nil, err
			}
			conflict.Files = append(conflict.Files, recoveryConflictFile{Path: path, Size: size, SHA256: hash, Item: status[path][0], Props: status[path][1]})
		}
		result = append(result, conflict)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

// Caller holds wcOpMu and has rechecked the no-effect transaction proof.
// All backups are flushed and verified BEFORE any resolve. A partial failure
// retains HOLD and every backup; subsequent plans inspect remaining conflicts.
// This function never publishes, deletes queue entries, or forces a lock.
func (s *Service) applyRecoveryConflicts(ctx context.Context, plan *commitRecoveryPlanState, in *commitIntent, releaseWriter func() error) error {
	current, err := s.inspectRecoveryConflicts(ctx, plan.headRevision)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, plan.conflicts) {
		return errors.New("conflicts or file contents changed; request a new plan")
	}
	if time.Now().After(plan.expires) {
		return errors.New("recovery plan expired during inspection")
	}
	if len(current) == 0 {
		return releaseWriter()
	}
	root := filepath.Join(s.wc, kolizjeDir)
	if err := os.Mkdir(root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if _, err := intentSafePath(s.wc, cacheEntry{Rel: kolizjeDir, Abs: root}); err != nil {
		return err
	}
	base := filepath.Join(s.wc, filepath.FromSlash(plan.copyDirectory))
	if err := os.Mkdir(base, 0700); err != nil {
		return err
	} // exclusive, never reuse another backup
	for _, conflict := range current {
		for _, file := range conflict.Files {
			src := filepath.Join(s.wc, filepath.FromSlash(file.Path))
			if err := saveConflictCopy(src, file.Path, base, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
			size, hash, err := intentFileHash(ctx, filepath.Join(base, filepath.FromSlash(file.Path)))
			if err != nil || size != file.Size || hash != file.SHA256 {
				return errors.New("conflict backup verification failed; HOLD retained")
			}
		}
	}
	// Persist where the copies went before SVN may consume its artifacts.
	in.ConflictCopies = append(in.ConflictCopies, plan.copyDirectory)
	for _, conflict := range current {
		for _, file := range conflict.Files {
			if file.Path != conflict.Path && file.Item == "added" {
				in.ConflictArtifacts = append(in.ConflictArtifacts, file.Path)
			}
		}
	}
	if err := s.writeIntent(s.wc, in); err != nil {
		return err
	}
	current, err = s.inspectRecoveryConflicts(ctx, plan.headRevision)
	if err != nil || !reflect.DeepEqual(current, plan.conflicts) {
		return errors.New("conflict changed while preserving copies; HOLD retained")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Native resolve obeys the same writer fence as commit. Release only this
	// proven no-effect attempt, after preserving every variant, before resolve.
	if err := releaseWriter(); err != nil {
		return err
	}
	for _, conflict := range current {
		if _, err := s.Cli.Resolve(ctx, s.wc, []string{conflict.Path}, "theirs-full"); err != nil {
			return err
		}
	}
	after, err := s.inspectRecoveryConflicts(ctx, plan.headRevision)
	if err != nil {
		return err
	}
	if len(after) != 0 {
		return errors.New("SVN still reports conflicts; HOLD retained")
	}
	if s.OnConflicts != nil {
		s.OnConflicts(0)
	}
	return nil
}

// SVN consumes its sidecars during resolve, but old clients may already have
// scheduled those same paths as additions. Unschedule only absent, backed-up
// artifacts recorded BEFORE resolve. Persisting that list also makes a crash
// between resolve and this cleanup recoverable without another content choice.
// An existing file, unknown status or a failed revert remains HOLD.
func (s *Service) cleanRecoveryArtifacts(ctx context.Context, in *commitIntent) error {
	if len(in.ConflictArtifacts) == 0 {
		return nil
	}
	reverter, ok := s.Cli.(pathReverter)
	if !ok {
		return errors.New("conflict artifact cleanup requires SVN revert")
	}
	for _, rel := range dedup(in.ConflictArtifacts) {
		abs, err := intentSafePath(s.wc, cacheEntry{Rel: rel, Abs: filepath.Join(s.wc, filepath.FromSlash(rel)), Op: "deleted"})
		if err != nil {
			return err
		}
		if _, err = os.Lstat(abs); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("conflict artifact reappeared: %s; HOLD retained", rel)
		}
		entries, err := s.Cli.Status(ctx, s.wc, []string{rel})
		if err != nil {
			return err
		}
		if len(entries) == 0 || (len(entries) == 1 && entries[0].Item == "none" && !entries[0].Conflicted) {
			continue
		}
		if len(entries) != 1 || entries[0].Item != "missing" || entries[0].Conflicted {
			return fmt.Errorf("conflict artifact status changed: %s", rel)
		}
		if _, err = reverter.Revert(ctx, s.wc, []string{rel}); err != nil {
			return err
		}
		if _, err = os.Lstat(abs); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("conflict artifact restored unexpectedly: %s", rel)
		}
	}
	return nil
}
