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

type NeedsLockClient interface {
	Status(context.Context, string, []string) ([]client.StatusEntry, error)
	PropList(context.Context, string, string) (map[string]bool, error)
	PropSet(context.Context, string, string, string, []string) (string, error)
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
