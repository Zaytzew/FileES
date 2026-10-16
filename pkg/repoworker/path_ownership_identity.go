package repoworker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// HEAD and public UUID are not an incarnation: a restored repository may
// deliberately retain both. Bind disposable history to the physical root and
// FSFS's UUID/instance record as well. This reads metadata only, never revision
// bodies or documents. Revprop authors are independently revalidated through
// SVN, including direct administrator writes that bypass FileES admission.
func repositoryHistoryIdentity(root, expectedUUID string) (string, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("ownership repository root is not a real directory")
	}
	f, err := os.Open(root)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(rootInfo, opened) {
		return "", errors.New("ownership repository root changed")
	}
	physical, err := historyPhysicalIdentity(f)
	if err != nil {
		return "", err
	}
	read := func(name string) ([]byte, error) {
		path := filepath.Join(root, "db", name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 1024 {
			return nil, errors.New("invalid ownership repository metadata")
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		opened, err := f.Stat()
		if err != nil || !os.SameFile(info, opened) {
			return nil, errors.New("ownership repository metadata changed")
		}
		return io.ReadAll(io.LimitReader(f, 1025))
	}
	fsType, err := read("fs-type")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(fsType)) != "fsfs" {
		return "", nil // unsupported storage: reconstruct instead of reusing history
	}
	raw, err := read("uuid")
	if err != nil {
		return "", err
	}
	lines := strings.Fields(string(raw))
	if len(raw) > 1024 || len(lines) < 1 || len(lines) > 2 || lines[0] != expectedUUID {
		return "", errors.New("ownership repository incarnation changed")
	}
	after, err := os.Lstat(root)
	if err != nil || !os.SameFile(opened, after) {
		return "", errors.New("ownership repository root changed")
	}
	sum := sha256.Sum256([]byte("filees.history-source/v1\x00" + physical + "\x00" + string(raw)))
	return hex.EncodeToString(sum[:]), nil
}
