package commit

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// No remote effect proves that retiring the native writer is safe, not that
// the same targets can be committed again. In particular, an older scheduled
// deletion may already have been removed remotely. A plain retry would fail
// again before polling catches up. Use the separate canonical decision rather
// than updating/reverting an uncertain WC or silently dropping its queue.
func (s *Service) inspectRecoveryDeletionBase(ctx context.Context, in *commitIntent, head int64) error {
	entries, err := s.Cli.Status(ctx, s.wc, in.Paths)
	if err != nil {
		return fmt.Errorf("inspect recovery targets: %w", err)
	}
	seen := make(map[string]bool)
	targets := make(map[string]bool, len(in.Paths))
	for _, target := range in.Paths {
		targets[target] = true
	}
	for _, entry := range entries {
		if entry.Item != "deleted" {
			continue
		}
		rel := strings.ReplaceAll(entry.Path, "\\", "/")
		if !safeIntentPath(rel) || seen[rel] {
			return fmt.Errorf("invalid deleted recovery target: %q", rel)
		}
		seen[rel] = true
		selected := false
		for parent := rel; parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if targets[parent] {
				selected = true
				break
			}
		}
		if !selected {
			return fmt.Errorf("unexpected deleted recovery target: %q", rel)
		}
		base, err := s.Cli.Revision(ctx, filepath.Join(s.wc, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("inspect deleted target base: %w", err)
		}
		if base != head {
			return fmt.Errorf("deleted recovery target %q has base r%d, repository r%d; reconcile the working copy before retry", rel, base, head)
		}
	}
	return nil
}
