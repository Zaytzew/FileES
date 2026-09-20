package nativeruntime

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const managedDirectory = "managed-v1"

// Lease pins the runtime for the daemon's whole lifetime, including idle time.
// The caller must retain it until all users have stopped. Child helpers inherit
// their own pin so a crashed parent cannot expose their libraries to GC.
type Lease struct {
	Path string
	file *os.File
}

func (l *Lease) Close() error { return l.file.Close() }

func leaseFile(path string, exclusive bool) (*os.File, error) {
	if err := plainPath(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if st, err := os.Lstat(path); err == nil {
		if !st.Mode().IsRegular() {
			return nil, errors.New("runtime lease is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return platformLease(path, exclusive)
}

func guard(ctx context.Context, root string) (*os.File, error) {
	for {
		f, err := leaseFile(filepath.Join(root, ".guard"), true)
		if !errors.Is(err, errLeaseBusy) {
			return f, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Acquire uses a new namespace: legacy processes never pinned their runtimes,
// so their directories must not be adopted into an automatically deleted pool.
func Acquire(ctx context.Context, cache string, payload []byte) (*Lease, error) {
	if !filepath.IsAbs(cache) {
		return nil, errors.New("runtime cache must be absolute")
	}
	root := filepath.Join(cache, managedDirectory)
	if err := plainPath(root); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	g, err := guard(ctx, root)
	if err != nil {
		return nil, err
	}
	defer g.Close()
	pin, err := leaseFile(filepath.Join(root, ".pin-"+ID(payload)), false)
	if err != nil {
		return nil, err
	}
	path, err := Ensure(root, payload)
	if err != nil {
		pin.Close()
		return nil, err
	}
	return &Lease{Path: path, file: pin}, nil
}

// PinCommand is a no-op for developer/system helpers. Managed helpers must
// exist under a pinned immutable runtime. Call after configuring SysProcAttr,
// before Start, and close the returned parent handle after Wait (or failure).
func PinCommand(cmd *exec.Cmd) (func(), error) {
	dir := filepath.Dir(cmd.Path)
	root := filepath.Dir(dir)
	if filepath.Base(root) != managedDirectory {
		return func() {}, nil
	}
	id := filepath.Base(dir)
	if !runtimeID(id) {
		return nil, errors.New("invalid managed runtime identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g, err := guard(ctx, root)
	if err != nil {
		return nil, err
	}
	defer g.Close()
	if err := plainPath(dir); err != nil {
		return nil, err
	}
	st, err := os.Lstat(cmd.Path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("managed helper is not regular")
	}
	pin, err := leaseFile(filepath.Join(root, ".pin-"+id), false)
	if err != nil {
		return nil, err
	}
	if err := inheritLease(cmd, pin); err != nil {
		pin.Close()
		return nil, err
	}
	return func() { _ = pin.Close() }, nil
}

func runtimeID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// Sweep deletes only recognized inactive managed runtimes/staging. All daemon
// and helper pins must be gone. Tiny lock files are retained to prevent split
// lock identities. Legacy caches and unknown contents are deliberately kept.
func Sweep(ctx context.Context, cache string) (int, error) {
	root := filepath.Join(cache, managedDirectory)
	if !filepath.IsAbs(cache) {
		return 0, errors.New("runtime cache must be absolute")
	}
	if err := plainPath(root); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	g, err := leaseFile(filepath.Join(root, ".guard"), true)
	if errors.Is(err, errLeaseBusy) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer g.Close()
	confined, err := os.OpenRoot(root)
	if err != nil {
		return 0, err
	}
	defer confined.Close()
	removed := 0
	var failures []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, errors.Join(append(failures, err)...)
		}
		name := entry.Name()
		if !entry.IsDir() || (!runtimeID(name) && !strings.HasPrefix(name, ".staging-")) {
			continue
		}
		err := func() error {
			if runtimeID(name) {
				// A directory without our lease marker is not owned by this protocol.
				if _, err := os.Lstat(filepath.Join(root, ".pin-"+name)); errors.Is(err, os.ErrNotExist) {
					return nil
				} else if err != nil {
					return err
				}
				pin, err := leaseFile(filepath.Join(root, ".pin-"+name), true)
				if errors.Is(err, errLeaseBusy) {
					return nil
				}
				if err != nil {
					return err
				}
				defer pin.Close()
			}
			target := filepath.Join(root, name)
			if err := plainPath(target); err != nil {
				return err
			}
			if err := filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				rel, err := filepath.Rel(target, path)
				if err != nil {
					return err
				}
				if rel == "." || (rel == "notices" && d.IsDir()) {
					return nil
				}
				if !d.Type().IsRegular() || !validName(filepath.ToSlash(rel)) {
					return errors.New("unrecognized runtime contents; kept")
				}
				return nil
			}); err != nil {
				return err
			}
			if err := confined.RemoveAll(name); err != nil {
				return err
			}
			removed++
			return nil
		}()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return removed, errors.Join(failures...)
}

var errLeaseBusy = errors.New("runtime is in use")
