package passport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"filees/pkg/client"
)

var ErrWorkingCopyDirty = errors.New("working copy must be clean before edit-passport migration")

// AppendOnlyProperty marks paths whose svn:needs-lock expresses append-only
// intent rather than this repository's editing policy - today the mobile
// upload channel, which is append-only by design and does not take part in
// edit passports at all. The rollback below leaves such paths untouched: the
// policy did not put the barrier there and must not take it away.
const AppendOnlyProperty = "filees:append-only"

type NeedsLockClient interface {
	Status(context.Context, string, []string) ([]client.StatusEntry, error)
	PropList(context.Context, string, string) (map[string]bool, error)
	PropSet(context.Context, string, string, string, []string) (string, error)
	PropDel(context.Context, string, string, []string) (string, error)
	Commit(context.Context, string, []string, string) (string, error)
}

// lockedElsewhere lists paths under wc that currently carry a repository lock,
// so a property migration can leave them alone.
//
// Both migrations commit a property change across many paths at once, and SVN
// refuses the *entire* commit if any single path is locked by someone else
// ("E160039: does not own lock"). One colleague editing one file would
// otherwise block the whole repository's migration - measured live on
// 2026-08-11, where one client's rollback failed wholesale because another
// held a single file.
//
// Locks are enumerated through the optional LockLister, which reaches the
// repository via `svn status --show-updates` rather than trusting the local
// working copy: a lock taken from a different machine is invisible to a plain
// `svn info` here. A client that cannot enumerate locks gets the previous
// all-or-nothing behaviour, which is the honest fallback - better to attempt
// and fail loudly than to silently skip everything.
func lockedElsewhere(ctx context.Context, cli NeedsLockClient, wc string) map[string]bool {
	lister, ok := cli.(client.LockLister)
	if !ok || lister == nil {
		return nil
	}
	entries, err := lister.ListLocks(ctx, wc)
	if err != nil {
		return nil
	}
	locked := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Token) == "" {
			continue
		}
		locked[filepath.ToSlash(filepath.Clean(entry.Path))] = true
	}
	return locked
}

// EnsureNeedsLock is an idempotent, restartable migration for existing files.
// It refuses content changes so a property-only commit can never publish user
// data that was edited before acquiring a passport.
func EnsureNeedsLock(ctx context.Context, cli NeedsLockClient, wc, instanceUID string, batchSize int) (skipped int, err error) {
	if cli == nil {
		return 0, errors.New("needs-lock migration: nil client")
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	status, err := cli.Status(ctx, wc, nil)
	if err != nil {
		return 0, err
	}
	existing, err := cli.PropList(ctx, wc, "svn:needs-lock")
	if err != nil {
		return 0, err
	}
	locked := lockedElsewhere(ctx, cli, wc)
	var setPaths, commitPaths []string
	for _, entry := range status {
		rel := filepath.ToSlash(filepath.Clean(entry.Path))
		if rel == "." || rel == ".filees" || strings.HasPrefix(rel, ".filees/") {
			continue
		}
		abs := filepath.Join(wc, filepath.FromSlash(rel))
		info, statErr := os.Stat(abs)
		if statErr != nil {
			if entry.Item == "missing" || entry.Item == "deleted" {
				return 0, fmt.Errorf("%w: %s (%s)", ErrWorkingCopyDirty, rel, entry.Item)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		switch entry.Item {
		case "normal":
		case "unversioned":
			continue
		default:
			return 0, fmt.Errorf("%w: %s (%s)", ErrWorkingCopyDirty, rel, entry.Item)
		}
		if locked[rel] {
			// Same reason as the rollback: one foreign lock would make SVN
			// refuse the whole commit. A path someone is holding will be
			// stamped on a later pass.
			skipped++
			continue
		}
		if !existing[rel] {
			setPaths = append(setPaths, rel)
			commitPaths = append(commitPaths, rel)
		} else if entry.Props == "modified" {
			commitPaths = append(commitPaths, rel)
		}
	}
	setPaths = uniqueSorted(setPaths)
	commitPaths = uniqueSorted(commitPaths)
	for start := 0; start < len(commitPaths); start += batchSize {
		end := start + batchSize
		if end > len(commitPaths) {
			end = len(commitPaths)
		}
		batch := commitPaths[start:end]
		var toSet []string
		wanted := make(map[string]bool, len(batch))
		for _, p := range batch {
			wanted[p] = true
		}
		for _, p := range setPaths {
			if wanted[p] {
				toSet = append(toSet, p)
			}
		}
		if len(toSet) > 0 {
			if out, err := cli.PropSet(ctx, wc, "svn:needs-lock", "*", toSet); err != nil {
				return skipped, fmt.Errorf("set svn:needs-lock: %w\n%s", err, out)
			}
		}
		message := fmt.Sprintf("FileES edit-passport migration by client %s: %d paths", instanceUID, len(batch))
		if out, err := cli.Commit(ctx, wc, batch, message); err != nil {
			return skipped, fmt.Errorf("commit svn:needs-lock migration: %w\n%s", err, out)
		}
	}
	return skipped, nil
}

// ClearNeedsLock is the reverse of EnsureNeedsLock and exists so that turning
// the editing policy off is actually reversible. svn:needs-lock is versioned,
// so without this the property outlives the policy and every client keeps
// seeing read-only files with no machinery left to unlock them - the
// repository would be permanently read-only.
//
// Callers must release the instance's own passports before calling this;
// removing the barrier under a live hold would leave a lock nobody renews.
// Like its counterpart it refuses to run on a dirty working copy so that a
// property-only commit can never smuggle out user content, and it is
// idempotent: paths that no longer carry the property are skipped, so a run
// interrupted halfway simply resumes.
func ClearNeedsLock(ctx context.Context, cli NeedsLockClient, wc, instanceUID string, batchSize int) (skipped int, err error) {
	if cli == nil {
		return 0, errors.New("needs-lock migration: nil client")
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	status, err := cli.Status(ctx, wc, nil)
	if err != nil {
		return 0, err
	}
	carrying, err := cli.PropList(ctx, wc, "svn:needs-lock")
	if err != nil {
		return 0, err
	}
	appendOnly, err := cli.PropList(ctx, wc, AppendOnlyProperty)
	if err != nil {
		return 0, err
	}
	locked := lockedElsewhere(ctx, cli, wc)
	var paths []string
	for _, entry := range status {
		rel := filepath.ToSlash(filepath.Clean(entry.Path))
		if rel == "." || rel == ".filees" || strings.HasPrefix(rel, ".filees/") {
			continue
		}
		switch entry.Item {
		case "normal":
		case "unversioned":
			continue
		default:
			return 0, fmt.Errorf("%w: %s (%s)", ErrWorkingCopyDirty, rel, entry.Item)
		}
		if !carrying[rel] || appendOnly[rel] {
			continue
		}
		if locked[rel] {
			// Somebody is working on this file right now. Leave the barrier
			// where it is and report the migration as incomplete, so the
			// caller retries rather than recording a rollback that only
			// partly happened.
			skipped++
			continue
		}
		paths = append(paths, rel)
	}
	paths = uniqueSorted(paths)
	for start := 0; start < len(paths); start += batchSize {
		end := start + batchSize
		if end > len(paths) {
			end = len(paths)
		}
		batch := paths[start:end]
		if out, err := cli.PropDel(ctx, wc, "svn:needs-lock", batch); err != nil {
			return skipped, fmt.Errorf("remove svn:needs-lock: %w\n%s", err, out)
		}
		message := fmt.Sprintf("FileES edit-passport rollback by client %s: %d paths", instanceUID, len(batch))
		if out, err := cli.Commit(ctx, wc, batch, message); err != nil {
			return skipped, fmt.Errorf("commit svn:needs-lock rollback: %w\n%s", err, out)
		}
	}
	return skipped, nil
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, p := range in {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
