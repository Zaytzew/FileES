package uploadworker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filees/pkg/avscan"
	"github.com/google/uuid"
)

func privateTrash(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestTrashFullKeepsIntakeAndRetention(t *testing.T) {
	r, job, calls := fixture(t, avscan.Infected)
	r.MaxTrashBytes = job.Size - 1
	got, err := r.Reap(context.Background())
	if !errors.Is(err, ErrTrashFull) || got.Failed != 1 || len(*calls) != 0 {
		t.Fatalf("%+v %v %v", got, err, *calls)
	}
	if err := verifyPayload(r.Intake.PayloadPath(job.UploadID), job); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.Intake.Root, job.UploadID, "READY")); err != nil {
		t.Fatal(err)
	}
	r.MaxTrashBytes = job.Size
	got, err = r.Reap(context.Background())
	if err != nil || got.Rejected != 1 {
		t.Fatalf("exact boundary: %+v %v", got, err)
	}
}

func TestTrashHiddenAndOrphanBytesCountUntilRemoved(t *testing.T) {
	root := privateTrash(t)
	now := time.Now().UTC()
	id := uuid.NewString()
	writeWaiting(t, root, "shelf/2026-09-30/"+id, Index{UploadID: id, OriginalName: "x", Size: 4, ReceivedAt: now, Hidden: true}, "data")
	r := Reaper{TrashRoot: root, MaxTrashBytes: 4}
	if err := r.checkTrashBudget(context.Background(), 1); !errors.Is(err, ErrTrashFull) {
		t.Fatal(err)
	}
	if err := r.PurgeExpired(context.Background(), now.Add(47*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := r.checkTrashBudget(context.Background(), 1); !errors.Is(err, ErrTrashFull) {
		t.Fatal(err)
	}
	if err := r.PurgeExpired(context.Background(), now.Add(49*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := r.checkTrashBudget(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".payload.tmp"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.checkTrashBudget(context.Background(), 1); !errors.Is(err, ErrTrashFull) {
		t.Fatal(err)
	}
}

func TestTrashRetryAfterPublisherFailureDoesNotCopyAgain(t *testing.T) {
	r, job, _ := fixture(t, avscan.Infected)
	r.Now = func() time.Time { return job.ReceivedAt }
	r.MaxTrashBytes = job.Size
	original := r.Publisher.Run
	r.Publisher.Run = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("synthetic publication failure")
	}
	got, err := r.Reap(context.Background())
	if err != nil || got.Failed != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	r.Publisher.Run = original
	got, err = r.Reap(context.Background())
	if err != nil || got.Rejected != 1 {
		t.Fatalf("retry: %+v %v", got, err)
	}
}

func TestRejectedCopyCannotOutgrowReservedBytes(t *testing.T) {
	dir := privateTrash(t)
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "payload")
	if err := os.WriteFile(src, []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst, 4); err == nil {
		t.Fatal("growing source accepted")
	}
	for _, p := range []string{dst, filepath.Join(dir, ".payload.tmp")} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("leftover %s: %v", p, err)
		}
	}
}

func TestTrashSweepPreservesUnknownFilesAndBadIndex(t *testing.T) {
	root := privateTrash(t)
	id := uuid.NewString()
	now := time.Now().UTC()
	rel := "shelf/2026-09-01/" + id
	writeWaiting(t, root, rel, Index{UploadID: id, OriginalName: "x", ReceivedAt: now.Add(-49 * time.Hour)}, "data")
	dir := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	r := Reaper{TrashRoot: root}
	if err := r.PurgeExpired(context.Background(), now); err == nil {
		t.Fatal("unknown file ignored")
	}
	for _, name := range []string{"keep", payloadName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, purgedLogName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused sweep wrote purge log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, indexName), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.PurgeExpired(context.Background(), now); err == nil {
		t.Fatal("bad metadata silently ignored")
	}
}

func TestTrashRejectsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix service path boundary")
	}
	root := privateTrash(t)
	out := privateTrash(t)
	if err := os.Symlink(out, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := (Reaper{TrashRoot: root, MaxTrashBytes: 100}).checkTrashBudget(context.Background(), 1); !errors.Is(err, ErrTrashState) {
		t.Fatal(err)
	}
}

func TestTrashBudgetAcrossProcesses(t *testing.T) {
	if root := os.Getenv("FILEES_TRASH_BUDGET_PROBE"); root != "" {
		r := Reaper{TrashRoot: root, MaxTrashBytes: 3}
		deadline := time.Now().Add(5 * time.Second)
		for {
			owner, err := r.ownTrash()
			if err != nil {
				if time.Now().After(deadline) {
					t.Fatal(err)
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}
			defer owner.Close()
			if err := r.checkTrashBudget(context.Background(), 1); errors.Is(err, ErrTrashFull) {
				return
			} else if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, uuid.NewString()), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	root := privateTrash(t)
	var commands []*exec.Cmd
	var outputs []*bytes.Buffer
	for i := 0; i < 8; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTrashBudgetAcrossProcesses$")
		cmd.Env = append(os.Environ(), "FILEES_TRASH_BUDGET_PROBE="+root)
		out := new(bytes.Buffer)
		cmd.Stdout = out
		cmd.Stderr = out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
		outputs = append(outputs, out)
	}
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("%s %v", outputs[i], err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("accepted %d, want 3", n)
	}
}
