package intake

import (
	"bytes"
	"errors"
	"github.com/google/uuid"
	"os"
	"testing"
)

func TestCapacityRefusalDoesNotReadBodyOrPublishReady(t *testing.T) {
	stop := errors.New("held")
	s := Store{Root: t.TempDir(), MaxBytes: 1 << 20, CheckWrite: func() error { return stop }}
	if _, err := s.Accept(uuid.NewString(), "realm", "slug", testToken, "file", unreadUploadBody{t}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	ready, err := s.ListReady()
	if err != nil || len(ready) != 0 {
		t.Fatal(ready, err)
	}
}

func TestCapacityChangeDuringReceiveCleansOnlyItsOwnJob(t *testing.T) {
	stop := errors.New("held")
	s := Store{Root: t.TempDir(), MaxBytes: 1 << 20, MaxQuarantineBytes: 10 << 20}
	kept, err := s.Accept(uuid.NewString(), "realm", "slug", testToken, "first", bytes.NewReader([]byte("kept")))
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	s.CheckWrite = func() error {
		checks++
		if checks >= 3 {
			return stop
		}
		return nil
	}
	if _, err := s.Accept(uuid.NewString(), "realm", "slug", testToken, "second", bytes.NewReader(bytes.Repeat([]byte{1}, 128<<10))); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	ready, err := s.ListReady()
	if err != nil || len(ready) != 1 || ready[0].UploadID != kept.UploadID {
		t.Fatal(ready, err)
	}
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != kept.UploadID {
			t.Fatal("leftover receiving job", entry.Name())
		}
	}
}
