//go:build linux || freebsd || openbsd || netbsd

package trash

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// move follows the freedesktop.org trash specification in the home trash:
// the .trashinfo record is written first (exclusively), then the folder is
// renamed into Trash/files. Across filesystems a rename is impossible; then
// the desktop's own `gio trash` is asked, and without it nothing is removed.
func move(path string) error {
	home, err := homeTrash()
	if err != nil {
		return err
	}
	filesDir, infoDir := filepath.Join(home, "files"), filepath.Join(home, "info")
	for _, dir := range []string{filesDir, infoDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	base := filepath.Base(path)
	for attempt := 0; attempt < 1000; attempt++ {
		name := base
		if attempt > 0 {
			name = base + "." + strconv.Itoa(attempt)
		}
		infoPath := filepath.Join(infoDir, name+".trashinfo")
		info, err := os.OpenFile(infoPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		escaped := (&url.URL{Path: path}).EscapedPath()
		_, writeErr := fmt.Fprintf(info, "[Trash Info]\nPath=%s\nDeletionDate=%s\n", escaped, time.Now().Format("2006-01-02T15:04:05"))
		closeErr := info.Close()
		if writeErr != nil || closeErr != nil {
			os.Remove(infoPath)
			return errors.Join(writeErr, closeErr)
		}
		if _, err := os.Lstat(filepath.Join(filesDir, name)); err == nil {
			os.Remove(infoPath)
			continue
		}
		err = os.Rename(path, filepath.Join(filesDir, name))
		if err == nil {
			return nil
		}
		os.Remove(infoPath)
		if errors.Is(err, syscall.EXDEV) {
			return gioTrash(path)
		}
		return err
	}
	return errors.New("recycle bin: no free name in the trash")
}

func homeTrash() (string, error) {
	if data := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); filepath.IsAbs(data) {
		return filepath.Join(data, "Trash"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "Trash"), nil
}

func gioTrash(path string) error {
	gio, err := exec.LookPath("gio")
	if err != nil {
		return fmt.Errorf("recycle bin: %s is on another filesystem than the trash and gio is unavailable; nothing was removed", path)
	}
	if out, err := exec.Command(gio, "trash", "--", path).CombinedOutput(); err != nil {
		return fmt.Errorf("recycle bin: gio trash %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}
