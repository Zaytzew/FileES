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

// EnsureNeedsLock is an idempotent, restartable migration for existing files.
// It refuses content changes so a property-only commit can never publish user
// data that was edited before acquiring a passport.
func EnsureNeedsLock(ctx context.Context, cli NeedsLockClient, wc, instanceUID string, batchSize int) error {
	if cli == nil {
		return errors.New("needs-lock migration: nil client")
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	status, err := cli.Status(ctx, wc, nil)
	if err != nil {
		return err
	}
	existing, err := cli.PropList(ctx, wc, "svn:needs-lock")
	if err != nil {
		return err
	}
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
				return fmt.Errorf("%w: %s (%s)", ErrWorkingCopyDirty, rel, entry.Item)
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
			return fmt.Errorf("%w: %s (%s)", ErrWorkingCopyDirty, rel, entry.Item)
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
				return fmt.Errorf("set svn:needs-lock: %w\n%s", err, out)
			}
		}
		message := fmt.Sprintf("FileES edit-passport migration by client %s: %d paths", instanceUID, len(batch))
		if out, err := cli.Commit(ctx, wc, batch, message); err != nil {
			return fmt.Errorf("commit svn:needs-lock migration: %w\n%s", err, out)
		}
	}
	return nil
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
func ClearNeedsLock(ctx context.Context, cli NeedsLockClient, wc, instanceUID string, batchSize int) error {
	if cli == nil {
		return errors.New("needs-lock migration: nil client")
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	status, err := cli.Status(ctx, wc, nil)
	if err != nil {
		return err
	}
	carrying, err := cli.PropList(ctx, wc, "svn:needs-lock")
	if err != nil {
		return err
	}
	appendOnly, err := cli.PropList(ctx, wc, AppendOnlyProperty)
	if err != nil {
		return err
	}
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
			return fmt.Errorf("%w: %s (%s)", ErrWorkingCopyDirty, rel, entry.Item)
		}
		if carrying[rel] && !appendOnly[rel] {
			paths = append(paths, rel)
		}
	}
	paths = uniqueSorted(paths)
	for start := 0; start < len(paths); start += batchSize {
		end := start + batchSize
		if end > len(paths) {
			end = len(paths)
		}
		batch := paths[start:end]
		if out, err := cli.PropDel(ctx, wc, "svn:needs-lock", batch); err != nil {
			return fmt.Errorf("remove svn:needs-lock: %w\n%s", err, out)
		}
		message := fmt.Sprintf("FileES edit-passport rollback by client %s: %d paths", instanceUID, len(batch))
		if out, err := cli.Commit(ctx, wc, batch, message); err != nil {
			return fmt.Errorf("commit svn:needs-lock rollback: %w\n%s", err, out)
		}
	}
	return nil
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
