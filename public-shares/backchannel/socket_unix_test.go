//go:build !windows

package backchannel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
)

func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fes-bc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "authority.sock")
}

func TestProtectedSocketRoundTripAndLivePreserved(t *testing.T) {
	path := socketPath(t)
	ln, cleanup, err := ListenUnix(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })}
	go server.Serve(ln)
	defer server.Close()
	owned, _ := os.Lstat(path)
	if _, _, err := ListenUnix(path, ""); err == nil {
		t.Fatal("replaced live listener")
	}
	now, _ := os.Lstat(path)
	if !os.SameFile(owned, now) {
		t.Fatal("live socket inode changed")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return DialUnix(ctx, path) }}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport}).Get("http://authority/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if string(raw) != "ok" {
		t.Fatalf("response=%q", raw)
	}
	if owned.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v", owned.Mode())
	}
}

func TestProtectedSocketStaleAndReplacementCleanup(t *testing.T) {
	path := socketPath(t)
	old, cleanup, err := ListenUnix(path, "")
	if err != nil {
		t.Fatal(err)
	}
	old.Close() // crash-shaped leftover, before the owning cleanup runs
	ln, currentCleanup, err := ListenUnix(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer currentCleanup()
	defer ln.Close()
	cleanup() // old process cannot remove the new process's socket
	if _, err := checkedSocket(path); err != nil {
		t.Fatal(err)
	}
	currentCleanup()
	if _, err := DialUnix(context.Background(), path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing tunnel: %v", err)
	}
}

func TestProtectedSocketRejectsUnsafePathsWithoutMutation(t *testing.T) {
	for _, mode := range []os.FileMode{0755, 0770, 0777, 0750} {
		t.Run(mode.String(), func(t *testing.T) {
			path := socketPath(t)
			dir := filepath.Dir(path)
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ListenUnix(path, ""); err == nil {
				t.Fatal("unsafe directory accepted")
			}
			info, _ := os.Stat(dir)
			if info.Mode().Perm() != mode {
				t.Fatal("permissions silently changed")
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("endpoint created")
			}
		})
	}
	for _, symlink := range []bool{false, true} {
		path := socketPath(t)
		target := filepath.Join(filepath.Dir(path), "keep")
		if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if symlink {
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := ListenUnix(path, ""); err == nil {
			t.Fatal("non-socket accepted")
		}
		if raw, _ := os.ReadFile(path); string(raw) != "keep" {
			t.Fatal("existing file changed")
		}
	}
}

func TestProtectedSocketGroupAndClientChecks(t *testing.T) {
	path := socketPath(t)
	group, err := user.LookupGroupId(strconv.Itoa(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	// OpenBSD inherits the parent directory group rather than our primary GID.
	if err := os.Chown(filepath.Dir(path), -1, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	ln, cleanup, err := ListenUnix(path, group.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer ln.Close()
	info, err := checkedSocket(path)
	if err != nil || info.Mode().Perm() != 0660 {
		t.Fatalf("group socket: %v %v", info, err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if conn, err := DialUnix(context.Background(), path); err == nil {
		conn.Close()
		t.Fatal("world-accessible endpoint accepted")
	}
	if err := os.Chmod(path, 0660); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0770); err != nil {
		t.Fatal(err)
	}
	if conn, err := DialUnix(context.Background(), path); err == nil {
		conn.Close()
		t.Fatal("replaceable endpoint accepted")
	}
}

func TestPreparedDialerChecksEachNewConnection(t *testing.T) {
	path := socketPath(t)
	dial, err := NewUnixDialer(path) // authority can start after links
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dial(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing endpoint: %v", err)
	}
	ln, cleanup, err := ListenUnix(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer ln.Close()
	conn, err := dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if conn, err := dial(context.Background()); err == nil {
		conn.Close()
		t.Fatal("prepared dialer accepted changed unsafe permissions")
	}
}
