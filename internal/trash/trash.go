// Package trash moves a folder to the desktop's recycle bin, never deleting
// it outright. It exists for "Usuń również lokalny folder" when a folder is
// detached from FileES (owner, 2026-09-28): the user can still take it back.
package trash

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrCancelled: the desktop asked for confirmation (the item would have been
// deleted permanently, e.g. too large for the recycle bin) and the user
// declined. Nothing was removed.
var ErrCancelled = errors.New("moving to the recycle bin was cancelled")

// Move moves path to the recycle bin. A path that no longer exists is done
// already, so a resumed operation can call it again.
func Move(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("recycle bin: path must be absolute: %q", path)
	}
	clean := filepath.Clean(path)
	if clean == filepath.Dir(clean) {
		return errors.New("recycle bin: a filesystem root cannot be moved")
	}
	if _, err := os.Lstat(clean); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := move(clean); err != nil {
		return err
	}
	if _, err := os.Lstat(clean); err == nil {
		return fmt.Errorf("recycle bin: %s is still present", clean)
	}
	return nil
}
