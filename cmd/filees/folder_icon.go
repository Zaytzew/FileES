package main

import (
	_ "embed"
	"os"
	"path/filepath"

	"filees/pkg/clientprofile"
)

// The folder icon shown on every working copy FileES keeps.
//
// Embedded rather than installed beside the binary because Explorer stores the
// icon's path, not the icon: a file that moved or vanished with the next client
// update would leave every managed folder rendering as a blank rectangle. The
// daemon writes it once into its own state directory, which no installer
// touches, and points every folder at that one copy.
//
//go:embed assets/filees-folder.ico
var managedFolderIcon []byte

// managedFolderIconPath returns the on-disk icon, writing it if needed.
//
// The write is content-checked rather than unconditional. This runs on every
// repository start, and rewriting the file each time would invalidate the
// shell's icon cache for every managed folder on every daemon restart.
func managedFolderIconPath() (string, error) {
	dir := filepath.Dir(clientprofile.DefaultRoot())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "filees-folder.ico")
	if existing, err := os.ReadFile(path); err == nil && len(existing) == len(managedFolderIcon) {
		return path, nil
	}
	if err := os.WriteFile(path, managedFolderIcon, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
