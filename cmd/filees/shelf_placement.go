package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filees/internal/durable"
	"filees/pkg/localrepo"
	"filees/pkg/portablepath"
)

// Publish a complete verified copy with an atomic no-replace hard link. Both
// paths are on the destination filesystem. No fallback to overwrite/rename.
// Keeping the staging link until the durable receipt makes a crash after the
// link distinguishable from an unrelated pre-existing destination file.
func placeShelfFile(source string, fetch localrepo.ShelfFetch) error {
	p := fetch.Placement
	if !filepath.IsAbs(p.ParentRoot) || !localrepo.ValidShelfPath(p.RelativePath) || !localrepo.ValidShelfPath(fetch.ID) {
		return errors.New("invalid placement")
	}
	if err := validateShelfDestination(p.ParentRoot, p.RelativePath); err != nil {
		return err
	}
	destination := filepath.Join(p.ParentRoot, filepath.FromSlash(p.RelativePath))
	staging := filepath.Join(p.ParentRoot, ".filees", "shelf-imports", fetch.ID)
	if err := shelfSafeTarget(p.ParentRoot, ".filees/shelf-imports/"+fetch.ID); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(staging), 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil {
		staged, stageErr := os.Lstat(staging)
		if stageErr != nil || !os.SameFile(info, staged) {
			return errors.New("destination already exists; nothing overwritten")
		}
		return verifyShelfCopy(destination, fetch)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Never truncate a stage: a concurrent user move could leave another link.
	// Only discard this operation's private partial staging entry on retry.
	if err := os.Remove(staging); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(output, hash), input)
	if err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if n != fetch.Size || hex.EncodeToString(hash.Sum(nil)) != fetch.SHA256 {
		return errors.New("shelf copy checksum mismatch")
	}
	if err := validateShelfDestination(p.ParentRoot, p.RelativePath); err != nil {
		return err
	}
	if err := os.Link(staging, destination); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(destination))
}

func validateShelfDestination(root, relative string) error {
	if !filepath.IsAbs(root) || !localrepo.ValidShelfPath(relative) {
		return errors.New("destination must be inside parent working copy")
	}
	if err := validateIdentityPath(root); err != nil {
		return err
	}
	if err := shelfSafeTarget(root, relative); err != nil {
		return err
	}
	parts := strings.Split(relative, "/")
	dir := root
	for i, part := range parts {
		if portablepath.SegmentProblem(part) != nil {
			return errors.New("destination name is not portable")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() != part && portablepath.Collides(part, []string{entry.Name()}) != "" {
				return errors.New("destination name conflicts with an existing file")
			}
		}
		if i == len(parts)-1 {
			break
		}
		dir = filepath.Join(dir, part)
		if _, err := os.Lstat(filepath.Join(dir, ".svn")); !errors.Is(err, os.ErrNotExist) {
			return errors.New("destination crosses a nested working copy")
		}
	}
	return nil
}

func verifyShelfCopy(path string, fetch localrepo.ShelfFetch) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != fetch.Size || hex.EncodeToString(h.Sum(nil)) != fetch.SHA256 {
		return errors.New("placed file changed; manual review required")
	}
	return nil
}

func shelfPlacementPublished(fetch localrepo.ShelfFetch) (bool, error) {
	p := fetch.Placement
	if err := validateShelfDestination(p.ParentRoot, p.RelativePath); err != nil {
		return false, err
	}
	if err := shelfSafeTarget(p.ParentRoot, ".filees/shelf-imports/"+fetch.ID); err != nil {
		return false, err
	}
	destination := filepath.Join(p.ParentRoot, filepath.FromSlash(p.RelativePath))
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stage, err := os.Lstat(filepath.Join(p.ParentRoot, ".filees", "shelf-imports", fetch.ID))
	if err != nil || !os.SameFile(info, stage) {
		return false, errors.New("destination already exists; nothing overwritten")
	}
	return true, verifyShelfCopy(destination, fetch)
}

func cleanupShelfStage(fetch localrepo.ShelfFetch) {
	p := fetch.Placement
	if p.ParentRepoID == "" || !filepath.IsAbs(p.ParentRoot) || !localrepo.ValidShelfPath(fetch.ID) || strings.Contains(fetch.ID, "/") {
		return
	}
	if validateIdentityPath(p.ParentRoot) != nil || shelfSafeTarget(p.ParentRoot, ".filees/shelf-imports/"+fetch.ID) != nil {
		return
	}
	// Only this operation's private link, never the destination or source.
	_ = os.Remove(filepath.Join(p.ParentRoot, ".filees", "shelf-imports", fetch.ID))
}
