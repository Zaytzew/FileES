package servertool

import (
	"filees/internal/mobileworker"
	"fmt"
	"os"
	"path/filepath"
)

// The forced command serves one request per process. Set TMPDIR before unveil:
// spool, unzip, sparse WC and SVN/APR children must all use the same filesystem.
// Never repair/chmod an operator path or fall back after an explicit selection.
func prepareMobileTemp(root string) (func(), error) {
	if root == "" {
		return func() {}, nil
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return nil, fmt.Errorf("must be an absolute dedicated directory")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%s must be a private directory (0700), not a symlink", root)
	}
	probe, err := os.CreateTemp(root, ".filees-write-check-")
	if err != nil && !mobileworker.IsStorageFull(err) {
		return nil, err
	}
	// Let a full filesystem reach the framed dispatcher, which can return
	// storage.full after reading the request. Other configuration errors fail here.
	if probe != nil {
		closeErr := probe.Close()
		removeErr := os.Remove(probe.Name())
		if closeErr != nil {
			return nil, closeErr
		}
		if removeErr != nil {
			return nil, removeErr
		}
	}
	previous, present := os.LookupEnv("TMPDIR")
	if err := os.Setenv("TMPDIR", root); err != nil {
		return nil, err
	}
	return func() {
		if present {
			_ = os.Setenv("TMPDIR", previous)
		} else {
			_ = os.Unsetenv("TMPDIR")
		}
	}, nil
}
