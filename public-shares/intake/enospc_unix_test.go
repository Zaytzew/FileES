//go:build linux || openbsd

package intake

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"filees/internal/storagewatch"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type capacityZeroReader struct{}

func (capacityZeroReader) Read(b []byte) (int, error) {
	clear(b)
	return len(b), nil
}

func TestIntakeActualENOSPCPreservesReadyAndProcessing(t *testing.T) {
	root := os.Getenv("FILEES_TEST_CAPACITY_FULL_FS")
	if root == "" {
		t.Skip("requires an empty disposable small filesystem: FILEES_TEST_CAPACITY_FULL_FS")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || err != nil || resolved != root {
		t.Fatal("unsafe fixture mount", err)
	}
	native := storagewatch.Native{}
	device, err := native.Device(root)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := native.Device(filepath.Dir(root))
	if err != nil || parent == device {
		t.Fatal("fixture must be a separate mount", err)
	}
	fs, err := native.Sample(root)
	if err != nil || fs.Total < 8<<20 || fs.Total > 128<<20 {
		t.Fatal("fixture must be 8–128 MiB", err, fs.Total)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("fixture must be empty", err)
	}
	dir, err := os.MkdirTemp(root, "intake-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	s := Store{Root: dir, MaxBytes: int64(fs.Total) * 2}
	ready, err := accept(t, s, uuid.NewString(), "ready-data")
	if err != nil {
		t.Fatal(err)
	}
	processing, err := accept(t, s, uuid.NewString(), "processing-data")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(processing.UploadID); err != nil {
		t.Fatal(err)
	}
	_, err = s.Accept(uuid.NewString(), "fixture", "fixture", testToken, "full.bin", io.LimitReader(capacityZeroReader{}, int64(fs.Total)*2))
	if !errors.Is(err, unix.ENOSPC) {
		t.Fatal("receive did not encounter actual ENOSPC", err)
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	jobs := 0
	for _, entry := range entries {
		if entry.IsDir() {
			jobs++
		}
	}
	if jobs != 2 {
		t.Fatal("failed receive leaked a job or removed unrelated data", jobs)
	}
	for _, tc := range []struct {
		job          Record
		marker, data string
	}{
		{ready, "READY", "ready-data"}, {processing, "PROCESSING", "processing-data"},
	} {
		if _, err := os.Stat(filepath.Join(dir, tc.job.UploadID, tc.marker)); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(s.PayloadPath(tc.job.UploadID))
		if err != nil || string(got) != tc.data {
			t.Fatal("existing payload changed", err)
		}
	}
	if _, err := accept(t, s, uuid.NewString(), "after-full"); err != nil {
		t.Fatal("failed receive did not free its own spool", err)
	}
}
