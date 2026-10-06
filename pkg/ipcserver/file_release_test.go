package ipcserver

import (
	"context"
	"errors"
	"testing"
)

func TestInternalFileReleaseCallbackRequiresAttachedWriter(t *testing.T) {
	rs := &RepoState{}
	op := func() error { return nil }
	if err := rs.WithFileRelease(t.Context(), "file", false, op); !errors.Is(err, ErrDepthUnavailable) {
		t.Fatal(err)
	}
	want := errors.New("writer fence refused")
	rs.SetFileReleaseFunc(func(ctx context.Context, rel string, recovery bool, operation func() error, complete func() error) error {
		if rel != "file" || !recovery || operation == nil {
			t.Fatal("wrong admission arguments")
		}
		return want
	})
	if err := rs.WithFileRelease(t.Context(), "file", true, op); !errors.Is(err, want) {
		t.Fatal(err)
	}
	rs.SetFileReleaseFunc(nil)
	if err := rs.WithFileRelease(t.Context(), "file", true, op); !errors.Is(err, ErrDepthUnavailable) {
		t.Fatal(err)
	}
}
