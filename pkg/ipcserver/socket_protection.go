package ipcserver

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"filees/pkg/privatefile"
)

// No Accept runs until both the directory and socket are protected. A private
// parent also prevents another user connecting between bind and socket hardening.
func listenProtectedSocket(path string, protect func(string) error) (net.Listener, os.FileInfo, error) {
	if err := prepareSocketDirectory(filepath.Dir(path)); err != nil {
		return nil, nil, fmt.Errorf("protect IPC directory: %w", err)
	}
	listener, err := listenUnixSocket(path)
	if err != nil {
		return nil, nil, err
	}
	owned, err := os.Stat(path)
	if err != nil {
		_ = listener.Close()
		return nil, nil, fmt.Errorf("inspect IPC socket: %w", err)
	}
	if err := protect(path); err != nil {
		_ = listener.Close()
		removeSocketIfOwned(path, owned)
		return nil, nil, fmt.Errorf("protect IPC socket: %w", err)
	}
	return listener, owned, nil
}

func protectSocket(path string) error {
	if err := privatefile.Harden(path); err != nil {
		return err
	}
	return privatefile.Verify(path)
}

func prepareSocketDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("IPC parent must be a real private directory")
	}
	// Harden only a newly created leaf or FileES' own state directory. Never
	// change permissions of an existing shared directory (e.g. /tmp, a drive
	// root or a custom XDG_RUNTIME_DIR); require it to be private instead.
	home, homeErr := os.UserHomeDir()
	dedicated := homeErr == nil && filepath.Clean(path) == filepath.Join(home, ".filees")
	if errors.Is(err, os.ErrNotExist) || dedicated {
		if err := privatefile.EnsureDir(path); err != nil {
			return err
		}
	}
	return privatefile.Verify(path)
}
