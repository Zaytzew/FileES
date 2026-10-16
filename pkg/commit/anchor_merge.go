package commit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"filees/pkg/client"
	"filees/pkg/cloudfiles"
	contract "filees/pkg/contract/v1"
)

type anchorMergeClient interface {
	HistoryList(context.Context, string, int64) ([]client.HistoryEntry, error)
	UpdateAdoptPathDepth(context.Context, string, string, int64, string) (string, error)
	UnschedulePlainAdds(context.Context, string, []string) error
}

// Caller holds wcOpMu and publication admission, after recovery. One pinned
// remote view classifies the copied paths and their parents BEFORE svn add.
// A failed/unknown view never becomes proof of a new path. No whole-directory
// hydration, server mutation, or uncertain commit replay happens here.
func (s *Service) prepareAnchorMerge(ctx context.Context, wc string, candidates []string) error {
	if !s.Anchor || len(candidates) == 0 {
		return nil
	}
	c, ok := s.Cli.(anchorMergeClient)
	if !ok {
		return errors.New("virtual folder merge requires bounded SVN adoption")
	}
	selected := map[string]bool{}
	for _, rel := range candidates {
		if !safeIntentPath(rel) {
			return errors.New("invalid virtual folder merge path")
		}
		abs := filepath.Join(wc, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() && (cloudfiles.NotOnDisk(abs) || cloudfiles.RecallsOnRead(info) || cloudfiles.IsPlaceholder(abs)) {
			continue // Seeded remote paths belong to the existing placeholder filter.
		}
		for p := rel; p != "."; p = path.Dir(p) {
			selected[p] = true
		}
	}
	paths := make([]string, 0, len(selected))
	for p := range selected {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool {
		a, b := strings.Count(paths[i], "/"), strings.Count(paths[j], "/")
		if a != b {
			return a < b
		}
		return paths[i] < paths[j]
	})
	status, err := s.statusMap(ctx, wc, paths)
	if err != nil {
		return err
	}
	needed := false
	for _, p := range paths {
		switch status[p] {
		case "", "none", "unversioned", "added":
			needed = true
		}
	}
	if !needed {
		return nil
	}
	report := func(phase string, done, total int) {
		if s.OnReconcileProgress != nil {
			s.OnReconcileProgress(&contract.ReconcileProgress{Phase: phase, Done: done, Total: total})
		}
	}
	report("planning", 0, 0)
	defer func() {
		if s.OnReconcileProgress != nil {
			s.OnReconcileProgress(nil)
		}
	}()
	if s.AnchorMergeRevision == nil {
		return errors.New("virtual folder merge has no bound remote revision")
	}
	revision, err := s.AnchorMergeRevision()
	if err != nil {
		return fmt.Errorf("virtual folder publication: bound remote baseline unavailable: %w", err)
	}
	if revision < 0 {
		return errors.New("invalid virtual folder merge revision")
	}
	u, err := url.Parse(s.RepoURL)
	if err != nil || u.Scheme == "" || u.Host == "" && u.Scheme != "file" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid virtual folder merge repository URL")
	}
	kinds, listed := map[string]string{".": "dir"}, map[string]bool{}
	// One pinned metadata-only tree replaces one remote session per parent.
	// Older test adapters retain the narrow listing path; production clients
	// must surface a failed tree read rather than treating it as an empty tree.
	wholeTree := false
	if revision == 0 {
		if _, ok := s.Cli.(client.HistoryTreeReader); !ok {
			return errors.New("empty virtual folder baseline requires an explicit revision-zero tree read")
		}
	}
	if reader, ok := s.Cli.(client.HistoryTreeReader); ok {
		dir, err := os.MkdirTemp("", "filees-merge-plan-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		file := filepath.Join(dir, "tree.jsonl")
		if _, err := reader.HistoryListTree(ctx, s.RepoURL, revision, file); err != nil {
			return err
		}
		if err := client.EachHistoryTreeNode(file, func(node client.HistoryTreeNode) error {
			if revision == 0 {
				return errors.New("revision-zero repository tree is not empty")
			}
			if !safeIntentPath(node.Path) {
				return errors.New("invalid virtual folder merge remote path")
			}
			if _, exists := kinds[node.Path]; exists {
				return errors.New("duplicate virtual folder merge remote path")
			}
			kinds[node.Path] = node.Kind
			return nil
		}); err != nil {
			return err
		}
		for p := range kinds {
			if p != "." && kinds[path.Dir(p)] != "dir" {
				return errors.New("incomplete virtual folder merge remote tree")
			}
		}
		wholeTree = true
	}
	type adoption struct{ rel, depth string }
	var plan []adoption
	for _, p := range paths {
		parent := path.Dir(p)
		if kinds[parent] != "dir" {
			continue
		}
		if !wholeTree && !listed[parent] {
			target := *u
			if parent != "." {
				target.Path = strings.TrimRight(u.Path, "/") + "/" + parent
				target.RawPath = ""
			}
			entries, err := c.HistoryList(ctx, target.String(), revision)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if !safeIntentPath(entry.Name) || strings.Contains(entry.Name, "/") || entry.Kind != "dir" && entry.Kind != "file" {
					return errors.New("invalid virtual folder merge remote child")
				}
				rel := path.Join(parent, entry.Name)
				if _, exists := kinds[rel]; exists {
					return errors.New("duplicate virtual folder merge remote child")
				}
				kinds[rel] = entry.Kind
			}
			listed[parent] = true
		}
		kind := kinds[p]
		if kind == "" {
			continue
		} // Proven absent at this pinned revision.
		switch status[p] {
		case "normal", "modified":
			continue // Never shrink an adopted directory.
		case "", "none", "unversioned", "added":
		default:
			return fmt.Errorf("virtual folder merge refuses local status %q", status[p])
		}
		abs := filepath.Join(wc, filepath.FromSlash(p))
		info, err := os.Lstat(abs)
		if err != nil {
			return err
		}
		depth := "empty"
		if kind == "file" {
			if !info.Mode().IsRegular() || cloudfiles.NotOnDisk(abs) || cloudfiles.RecallsOnRead(info) || cloudfiles.IsPlaceholder(abs) {
				return errors.New("virtual folder merge requires an ordinary local file")
			}
			depth = "infinity"
		} else if !info.IsDir() {
			return errors.New("virtual folder merge kind mismatch")
		}
		plan = append(plan, adoption{p, depth})
	}
	if len(plan) > 0 {
		if check, ok := s.Cli.(interface{ CheckAdoptBatch(context.Context) error }); ok {
			if err := check.CheckAdoptBatch(ctx); err != nil {
				return err
			}
		}
		var scheduled []string
		for i := len(paths) - 1; i >= 0; i-- {
			if status[paths[i]] == "added" {
				scheduled = append(scheduled, paths[i])
			}
		}
		if len(scheduled) > 0 {
			if err := c.UnschedulePlainAdds(ctx, wc, scheduled); err != nil {
				return err
			}
		}
	}
	report("adopting", 0, len(plan))
	for i := 0; i < len(plan); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := i + 1
		chars := len(plan[i].rel)
		// Bounded command size, same depth, parent-first order. Confirm only
		// complete receipts; a failed partial batch is re-inspected on retry.
		for end < len(plan) && end-i < 16 && plan[end].depth == plan[i].depth && chars+len(plan[end].rel) < 12000 {
			chars += len(plan[end].rel)
			end++
		}
		if batch, ok := s.Cli.(interface {
			UpdateAdoptPathsDepth(context.Context, string, []string, int64, string) (string, error)
		}); ok {
			rels := make([]string, 0, end-i)
			for _, step := range plan[i:end] {
				rels = append(rels, step.rel)
			}
			if _, err := batch.UpdateAdoptPathsDepth(ctx, wc, rels, revision, plan[i].depth); err != nil {
				return err
			}
		} else {
			end = i + 1
			if _, err := c.UpdateAdoptPathDepth(ctx, wc, plan[i].rel, revision, plan[i].depth); err != nil {
				return err
			}
		}
		i = end
		report("adopting", i, len(plan))
	}
	return nil // Caller re-reads status and content proof before publication.
}
