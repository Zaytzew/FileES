package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filees/internal/serverinstall/manifest"
)

// verifyArchiveStaged checks a complete payload without executing candidate
// binaries. A signed assertion and successful staging are necessary evidence,
// not a substitute for durable admission, session/resident drain or recovery.
// In particular this must NEVER bypass withArchiveInstallAdmission.
func (r *Runner) verifyArchiveStaged(ctx context.Context, m *manifest.Manifest, staged []StagedFile, root string) error {
	if err := m.ValidateArchiveFormats(); err != nil {
		return err
	}
	if m.ArchiveFormats == nil {
		return nil
	}
	if len(staged) != len(m.Files) {
		return errors.New("archive staging does not contain the complete signed payload")
	}
	targets := make(map[string]bool, len(staged))
	for index, sf := range staged {
		if err := ctx.Err(); err != nil {
			return err
		}
		mf := m.Files[index]
		target := manifest.ResolveTarget(r.dirs(), mf.Target)
		mode, err := parseMode(mf.Mode, mf.Kind)
		if err != nil || !filepath.IsAbs(target) || sf.ManifestFile != mf || sf.Target != target || sf.Mode != mode ||
			!strings.EqualFold(sf.SHA256, mf.SHA256) || targets[target] {
			return fmt.Errorf("archive staged component binding differs: %s", mf.Target)
		}
		ownership, err := r.ownershipManager().Resolve(mf.Owner, mf.Group)
		if err != nil || sf.Ownership != ownership {
			return fmt.Errorf("archive staged component ownership differs: %s", mf.Target)
		}
		targets[target] = true
		expected := filepath.Join(root, filepath.FromSlash(mf.Source))
		if sf.StagePath != expected {
			return fmt.Errorf("archive staged source binding differs: %s", mf.Source)
		}
		if err := archiveStagedRegular(root, expected); err != nil {
			return err
		}
		info, err := os.Lstat(expected)
		if err != nil {
			return err
		}
		f, err := os.Open(expected)
		if err != nil {
			return err
		}
		opened, statErr := f.Stat()
		if statErr != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
			f.Close()
			return errors.New("archive staged file identity changed")
		}
		h := sha256.New()
		_, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), mf.SHA256) {
			return fmt.Errorf("archive staged payload hash differs: %s", mf.Source)
		}
	}
	return ctx.Err()
}

func archiveStagedRegular(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsAbs(root) || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("archive staged file escapes its private root")
	}
	current := root
	parts := append([]string{""}, strings.Split(rel, string(filepath.Separator))...)
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return fmt.Errorf("archive staging contains an alias or nonregular component: %s", current)
		}
	}
	return nil
}
