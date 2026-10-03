//go:build linux || openbsd

package storagewatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCapacityRootMonitorPreservesServiceOwner(t *testing.T) {
	if root := os.Getenv("FILEES_TEST_CAPACITY_OWNER_ROOT"); root != "" {
		g := Gate{Root: root}
		if phase, err := g.Phase(); phase != "held" || err != nil {
			t.Fatal(phase, err)
		}
		if err := g.WithWriter(context.Background(), func() error { return nil }); !errors.Is(err, ErrWriteHeld) {
			t.Fatal(err)
		}
		if err := g.WithExclusive(context.Background(), g.Resume); err != nil {
			t.Fatal(err)
		}
		if err := g.WithWriter(context.Background(), func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		if err := g.Hold(); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root to exercise service ownership")
	}
	account, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("no nobody test account")
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil || uid == 0 {
		t.Fatal("invalid unprivileged account", err)
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("", "filees-capacity-owner-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Chown(root, int(uid), int(gid)); err != nil {
		t.Fatal(err)
	}
	// The temporary Go build directory may be private to root. Copy only the
	// test executable into this disposable, private service-owned fixture.
	src, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	exe := filepath.Join(root, "owner.test")
	dst, err := os.OpenFile(exe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal(copyErr, closeErr)
	}
	if err := os.Chown(exe, int(uid), int(gid)); err != nil {
		t.Fatal(err)
	}
	g := Gate{Root: root}
	if err := g.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := g.Hold(); err != nil {
		t.Fatal(err)
	}
	if err := g.WithExclusive(context.Background(), g.AcknowledgeHeld); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestCapacityRootMonitorPreservesServiceOwner$")
	cmd.Env = append(os.Environ(), "FILEES_TEST_CAPACITY_OWNER_ROOT="+root)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal("service cannot use root-published gate", err, string(out))
	}
	for _, name := range []string{"capacity-write-policy.json", ".capacity-writers.lock", "capacity-active-writers"} {
		var st unix.Stat_t
		if err := unix.Stat(filepath.Join(root, name), &st); err != nil {
			t.Fatal(err)
		}
		if st.Uid != uint32(uid) || st.Gid != uint32(gid) {
			t.Fatal("root took ownership of service state", name, st.Uid, st.Gid)
		}
	}
}

func TestCapacityWriterKilledProcess(t *testing.T) {
	if root := os.Getenv("FILEES_TEST_KILLED_WRITER_ROOT"); root != "" {
		g := Gate{Root: root}
		if err := g.WithWriter(context.Background(), func() error {
			if _, err := os.Stdout.Write([]byte("ready")); err != nil {
				return err
			}
			_, err := io.Copy(io.Discard, os.Stdin)
			return err
		}); err != nil {
			os.Exit(99)
		}
		os.Exit(0)
	}
	g := Gate{Root: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCapacityWriterKilledProcess$")
	cmd.Env = append(os.Environ(), "FILEES_TEST_KILLED_WRITER_ROOT="+g.Root)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if _, err := io.ReadFull(output, make([]byte, 5)); err != nil {
		t.Fatal(err)
	}
	if err := g.Hold(); err != nil {
		t.Fatal(err)
	}
	waitCtx, stop := context.WithTimeout(ctx, 80*time.Millisecond)
	err = g.WithExclusive(waitCtx, g.AcknowledgeHeld)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("live writer did not hold its lease", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("writer was not killed")
	}
	// Killing the process releases flock, but never manufactures completion.
	for _, action := range []func() error{g.AcknowledgeHeld, g.Resume} {
		if err := g.WithExclusive(ctx, action); !errors.Is(err, ErrUnfinishedWriter) {
			t.Fatal("killed writer treated as completed", err)
		}
	}
	if phase, err := g.Phase(); phase != "draining" || err != nil {
		t.Fatal(phase, err)
	}
}

// Opt-in only: the caller supplies an EMPTY, separately mounted, disposable
// filesystem of 8–128 MiB. Never fill a system filesystem or a generic TMPDIR.
func TestCapacityGateActualENOSPC(t *testing.T) {
	root := os.Getenv("FILEES_TEST_CAPACITY_FULL_FS")
	if root == "" {
		t.Skip("requires a disposable small filesystem: FILEES_TEST_CAPACITY_FULL_FS")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		t.Fatal("fixture mount must be an absolute clean path")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		t.Fatal("fixture mount must not traverse symlinks", err)
	}
	var here, parent unix.Stat_t
	if err := unix.Stat(root, &here); err != nil {
		t.Fatal(err)
	}
	if err := unix.Stat(filepath.Dir(root), &parent); err != nil || here.Dev == parent.Dev {
		t.Fatal("fixture must be a separate mount", err)
	}
	fs, err := (Native{}).Sample(root)
	if err != nil {
		t.Fatal(err)
	}
	size := fs.Total
	if size < 8<<20 || size > 128<<20 {
		t.Fatal("refusing to fill a filesystem outside 8–128 MiB", size)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("fixture filesystem must be empty", err)
	}
	for _, initiallyHeld := range []bool{false, true} {
		name := "open"
		if initiallyHeld {
			name = "held"
		}
		t.Run(name, func(t *testing.T) {
			dir, err := os.MkdirTemp(root, "gate-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			g := Gate{Root: dir}
			if err := g.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			if initiallyHeld {
				if err := g.Hold(); err != nil {
					t.Fatal(err)
				}
				if err := g.WithExclusive(context.Background(), g.AcknowledgeHeld); err != nil {
					t.Fatal(err)
				}
			} else if err := g.Resume(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(dir, "capacity-write-policy.json"))
			if err != nil {
				t.Fatal(err)
			}
			filler := filepath.Join(dir, "filler")
			if err := os.Mkdir(filler, 0700); err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(filler)
			fillCapacityFixture(t, filler, size)
			var changeErr error
			if initiallyHeld {
				changeErr = g.WithExclusive(context.Background(), g.Resume)
			} else {
				changeErr = g.Hold()
			}
			if !errors.Is(changeErr, unix.ENOSPC) {
				t.Fatal("policy replacement did not encounter actual ENOSPC", changeErr)
			}
			after, err := os.ReadFile(filepath.Join(dir, "capacity-write-policy.json"))
			if err != nil || string(before) != string(after) {
				t.Fatal("failed policy replacement damaged the previous policy", err)
			}
			called := false
			err = g.WithWriter(context.Background(), func() error { called = true; return nil })
			if err == nil || called {
				t.Fatal("full filesystem admitted effects without a durable marker", err, called)
			}
			if err := os.RemoveAll(filler); err != nil {
				t.Fatal(err)
			}
			// The failed first Hold is not proof of a stop. Retry after space is
			// restored, then acknowledge and resume under the exclusive lease.
			if err := g.Hold(); err != nil {
				t.Fatal(err)
			}
			if err := g.WithExclusive(context.Background(), g.AcknowledgeHeld); err != nil {
				t.Fatal(err)
			}
			if err := g.WithExclusive(context.Background(), g.Resume); err != nil {
				t.Fatal(err)
			}
			if err := g.WithWriter(context.Background(), func() error { called = true; return nil }); err != nil || !called {
				t.Fatal("writer did not recover after freeing the fixture", err)
			}
		})
	}
}

func fillCapacityFixture(t *testing.T, path string, limit uint64) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(path, "data"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buffer := make([]byte, 512)
	for written := uint64(0); written <= limit+uint64(len(buffer)); {
		n, err := f.Write(buffer)
		written += uint64(n)
		if errors.Is(err, unix.ENOSPC) {
			t.Logf("actual ENOSPC after %d bytes", written)
			// FFS can still have free fragments that cannot extend this file
			// into its next full block. Consume those with separate small files
			// so this also tests a failed small metadata write.
			for i := uint64(0); i <= limit/uint64(len(buffer)); i++ {
				fragment, err := os.OpenFile(filepath.Join(path, fmt.Sprintf("fragment-%d", i)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if errors.Is(err, unix.ENOSPC) {
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				_, err = fragment.Write(buffer)
				closeErr := fragment.Close()
				if errors.Is(err, unix.ENOSPC) || errors.Is(closeErr, unix.ENOSPC) {
					return
				}
				if err != nil || closeErr != nil {
					t.Fatal(err, closeErr)
				}
			}
			t.Fatal("bounded fragment fill did not reach ENOSPC")
		}
		if err != nil || n != len(buffer) {
			t.Fatal("unexpected fixture write failure", err, n)
		}
	}
	t.Fatal("bounded fill did not reach ENOSPC")
}
