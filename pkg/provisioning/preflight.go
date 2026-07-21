package provisioning

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type LocalPathMode string

const (
	LocalPathCreate LocalPathMode = "create"
	LocalPathAttach LocalPathMode = "attach"
)

type LocalPathCheck struct {
	CanonicalPath string
	Exists        bool
	Empty         bool
}

// PreflightLocalPath validates a prospective FileES root without changing it.
// It resolves symlinks even when the final path does not exist, so aliases
// cannot bypass the disjoint-root check.
func PreflightLocalPath(path string, mode LocalPathMode, existingRoots []string) (LocalPathCheck, error) {
	if mode != LocalPathCreate && mode != LocalPathAttach {
		return LocalPathCheck{}, fmt.Errorf("unsupported local path mode %q", mode)
	}
	if !filepath.IsAbs(path) {
		return LocalPathCheck{}, errors.New("local path must be absolute")
	}
	path = filepath.Clean(path)
	if path == string(filepath.Separator) {
		return LocalPathCheck{}, errors.New("filesystem root cannot be a repository root")
	}

	canonical, err := canonicalProspectivePath(path)
	if err != nil {
		return LocalPathCheck{}, fmt.Errorf("resolve local path: %w", err)
	}
	if err := rejectWorkingCopy(canonical); err != nil {
		return LocalPathCheck{}, err
	}
	for _, root := range existingRoots {
		if !filepath.IsAbs(root) {
			return LocalPathCheck{}, fmt.Errorf("existing repository root must be absolute: %q", root)
		}
		other, err := canonicalProspectivePath(filepath.Clean(root))
		if err != nil {
			return LocalPathCheck{}, fmt.Errorf("resolve existing repository root %q: %w", root, err)
		}
		if pathsOverlap(canonical, other) {
			return LocalPathCheck{}, fmt.Errorf("local path %q overlaps existing repository root %q", canonical, other)
		}
	}

	info, err := os.Stat(canonical)
	if errors.Is(err, os.ErrNotExist) {
		return LocalPathCheck{CanonicalPath: canonical, Empty: true}, nil
	}
	if err != nil {
		return LocalPathCheck{}, fmt.Errorf("inspect local path: %w", err)
	}
	if !info.IsDir() {
		return LocalPathCheck{}, fmt.Errorf("local path is not a directory (%s)", fileType(info.Mode()))
	}
	empty, err := directoryEmpty(canonical)
	if err != nil {
		return LocalPathCheck{}, err
	}
	if mode == LocalPathAttach && !empty {
		return LocalPathCheck{}, errors.New("attach target must be absent or empty")
	}
	return LocalPathCheck{CanonicalPath: canonical, Exists: true, Empty: empty}, nil
}

func canonicalProspectivePath(path string) (string, error) {
	remaining := make([]string, 0)
	cursor := filepath.Clean(path)
	for {
		resolved, err := filepath.EvalSymlinks(cursor)
		if err == nil {
			parts := append([]string{resolved}, reverseStrings(remaining)...)
			return filepath.Clean(filepath.Join(parts...)), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return "", err
		}
		remaining = append(remaining, filepath.Base(cursor))
		cursor = parent
	}
}

func reverseStrings(values []string) []string {
	out := make([]string, len(values))
	for i := range values {
		out[len(values)-1-i] = values[i]
	}
	return out
}

func rejectWorkingCopy(path string) error {
	for cursor := path; ; cursor = filepath.Dir(cursor) {
		if info, err := os.Stat(filepath.Join(cursor, ".svn")); err == nil && info.IsDir() {
			return fmt.Errorf("local path is inside an existing Subversion working copy rooted at %q", cursor)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect Subversion metadata at %q: %w", cursor, err)
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return nil
		}
	}
}

func pathsOverlap(a, b string) bool {
	rel, err := filepath.Rel(a, b)
	if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
		return true
	}
	rel, err = filepath.Rel(b, a)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

func directoryEmpty(path string) (bool, error) {
	dir, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open local path: %w", err)
	}
	defer dir.Close()
	_, err = dir.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read local path: %w", err)
	}
	return false, nil
}

func fileType(mode fs.FileMode) string {
	switch {
	case mode.IsRegular():
		return "regular file"
	case mode&os.ModeNamedPipe != 0:
		return "named pipe"
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeDevice != 0:
		return "device"
	default:
		return mode.Type().String()
	}
}
