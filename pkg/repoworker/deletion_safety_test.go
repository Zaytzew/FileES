package repoworker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDeletionDumpWriterChecksEveryChunkAndStops(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	var out bytes.Buffer
	calls := 0
	w := deletionDumpWriter{ctx: ctx, cancel: cancel, out: &out, root: "archive", check: func(_ context.Context, root string, extra int64) error {
		calls++
		if root != "archive" || extra > deletionWriteChunk || extra <= 0 {
			t.Fatalf("root=%s extra=%d", root, extra)
		}
		if calls == 2 {
			return errDeletionCapacity
		}
		return nil
	}}
	n, err := w.Write(make([]byte, 3*deletionWriteChunk))
	if n != deletionWriteChunk || out.Len() != n || !errors.Is(err, errDeletionCapacity) || !errors.Is(context.Cause(ctx), errDeletionCapacity) {
		t.Fatalf("n=%d bytes=%d err=%v cause=%v", n, out.Len(), err, context.Cause(ctx))
	}
}

type shortDeletionWriter struct{}

func (shortDeletionWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

func TestDeletionDumpWriterShortWriteCancels(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	w := deletionDumpWriter{ctx: ctx, cancel: cancel, out: shortDeletionWriter{}, check: deletionSpaceOK}
	if _, err := w.Write([]byte("test")); !errors.Is(err, io.ErrShortWrite) || !errors.Is(context.Cause(ctx), io.ErrShortWrite) {
		t.Fatalf("err=%v cause=%v", err, context.Cause(ctx))
	}
}

func TestDeletionCancelledDigestAndReplayPreserveSource(t *testing.T) {
	e, repoID, repo := deletionFixture(t, 7, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fileDigestContext(ctx, filepath.Join(repo, "format")); !errors.Is(err, context.Canceled) {
		t.Fatalf("digest ignored cancellation: %v", err)
	}
	if _, err := e.ArchiveAndDeleteFSFS(ctx, repoID, uuid.NewString()); !errors.Is(err, context.Canceled) || !validRepo(repo) {
		t.Fatalf("err=%v source=%v", err, validRepo(repo))
	}
}

func deletionSpaceOK(context.Context, string, int64) error { return nil }

func TestDeletionPreflightPreservesRepository(t *testing.T) {
	for _, volume := range []string{"repositories", "archive"} {
		t.Run(volume, func(t *testing.T) {
			e, repoID, repo := deletionFixture(t, 7, time.Now())
			op := uuid.NewString()
			full := e.RepositoriesRoot
			if volume == "archive" {
				full = e.DeletionArchiveRoot
			}
			_, err := e.archiveAndDeleteFSFS(context.Background(), repoID, op, func(_ context.Context, root string, _ int64) error {
				if root == full {
					return errDeletionCapacity
				}
				return nil
			})
			if !errors.Is(err, errDeletionCapacity) || !validRepo(repo) {
				t.Fatalf("err=%v source valid=%v", err, validRepo(repo))
			}
			entries, err := os.ReadDir(e.DeletionArchiveRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("archive entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestDeletionLoadAdmissionKeepsDumpForRetry(t *testing.T) {
	e, repoID, repo := deletionFixture(t, 7, time.Now())
	op := uuid.NewString()
	_, err := e.archiveAndDeleteFSFS(context.Background(), repoID, op, func(_ context.Context, root string, extra int64) error {
		if root == e.RepositoriesRoot && extra > 0 {
			return errDeletionCapacity
		}
		return nil
	})
	dumpPath := filepath.Join(e.DeletionArchiveRoot, repoID+"-"+op+".svndump")
	if !errors.Is(err, errDeletionCapacity) || !validRepo(repo) {
		t.Fatalf("err=%v source valid=%v", err, validRepo(repo))
	}
	before, err := fileDigest(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.DeletionArchiveRoot, repoID+"-"+op+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("premature receipt: %v", err)
	}
	if _, err := e.archiveAndDeleteFSFS(context.Background(), repoID, op, deletionSpaceOK); err != nil {
		t.Fatal(err)
	}
	after, err := fileDigest(dumpPath)
	if err != nil || before != after {
		t.Fatalf("retry replaced dump: before=%s after=%s err=%v", before, after, err)
	}
}

// The helper process is deliberately quiet and bounded by the parent test's
// cancellation. It exercises the real os/exec path on Windows and OpenBSD.
func TestDeletionSafetyHelper(t *testing.T) {
	if os.Getenv("FILEES_DELETION_TEST_HELPER") != "1" {
		return
	}
	for {
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDeletionCommandMonitorCancelsIdleChild(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	calls := 0
	err = runDeletionCommand(ctx, t.TempDir(), func(context.Context, string, int64) error {
		calls++ // initial call precedes monitor; monitor is joined before return
		if calls > 1 {
			return errDeletionCapacity
		}
		return nil
	}, executable, []string{"-test.run=^TestDeletionSafetyHelper$"}, func(cmd *exec.Cmd, _ context.Context, _ context.CancelCauseFunc) {
		cmd.Env = append(os.Environ(), "FILEES_DELETION_TEST_HELPER=1")
	})
	if !errors.Is(err, errDeletionCapacity) || ctx.Err() != nil {
		t.Fatalf("err=%v outer=%v", err, ctx.Err())
	}
}

// Opt-in native acceptance on an isolated small filesystem, never a live
// server path. The test creates/removes only its own subdirectory.
func TestDeletionSmallFilesystemAdmission(t *testing.T) {
	root := os.Getenv("FILEES_DELETION_SMALL_FS")
	if root == "" {
		t.Skip("isolated small filesystem not supplied")
	}
	if !filepath.IsAbs(root) || !strings.Contains(filepath.Base(root), "filees-delete-lab") {
		t.Fatal("test root must be an explicit filees-delete-lab directory")
	}
	fixture, err := os.MkdirTemp(root, "admission-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(fixture)
	if err := checkDeletionSpace(context.Background(), fixture, 0); !errors.Is(err, errDeletionCapacity) {
		t.Fatalf("expected capacity refusal on small volume: %v", err)
	}
	e, repoID, repo := deletionFixture(t, 7, time.Now())
	e.DeletionArchiveRoot = filepath.Join(fixture, "archives")
	if _, err := e.ArchiveAndDeleteFSFS(context.Background(), repoID, uuid.NewString()); !errors.Is(err, errDeletionCapacity) || !validRepo(repo) {
		t.Fatalf("native small-volume deletion err=%v source=%v", err, validRepo(repo))
	}
}
