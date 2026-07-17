package svnrotate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// writeFileExcl creates path exclusively (never truncating an existing
// artifact), writes data, and syncs it to stable storage. Close errors are
// checked — a truncated archive artifact must never pass silently.
func writeFileExcl(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// createExcl opens a new file for streaming writes with O_EXCL.
func createExcl(path string, mode os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
}

// fsyncDir syncs a directory so a completed rename survives power loss.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil {
		return fmt.Errorf("fsync %s: %w", dir, syncErr)
	}
	return closeErr
}

// copyTree copies every regular file under srcDir to the same relative path
// under dstDir, preserving file modes and overwriting existing files (the
// destination starts as svnadmin create's defaults). Missing srcDir is not
// an error — a repo without a conf/ customisation is legal.
func copyTree(srcDir, dstDir string) error {
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDir, rel)
		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFileTo(path, target, info.Mode().Perm())
	})
}

func copyFileTo(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(dst, mode)
}
