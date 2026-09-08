//go:build !windows

package repoworker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func deletionScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "svnadmin-test")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDeletionDumpCapacityCancelsFreezeAndChild(t *testing.T) {
	e, _, repo := deletionFixture(t, 7, time.Now())
	e.SVNAdmin = deletionScript(t, `if [ "$1" = freeze ]; then
  "$0" dump "$2" &
  wait
else
  trap '' TERM
  while :; do printf 'bounded dump output\n'; done
fi
`)
	if err := os.MkdirAll(e.DeletionArchiveRoot, 0700); err != nil {
		t.Fatal(err)
	}
	op := uuid.NewString()
	final := filepath.Join(e.DeletionArchiveRoot, "test.svndump")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := e.createDump(ctx, repo, final, op, func(_ context.Context, _ string, extra int64) error {
		if extra > 0 {
			return errDeletionCapacity
		}
		return nil
	})
	if !errors.Is(err, errDeletionCapacity) || ctx.Err() != nil || !validRepo(repo) {
		t.Fatalf("err=%v outer=%v source=%v", err, ctx.Err(), validRepo(repo))
	}
	for _, path := range []string{final, final + ".tmp-" + op} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial dump survived: %s %v", path, err)
		}
	}
}

func TestDeletionLoadMonitorCleansScratchPreservesDump(t *testing.T) {
	e, _, repo := deletionFixture(t, 7, time.Now())
	e.SVNAdmin = deletionScript(t, `case "$1" in
create) mkdir -p "$2" ;;
load) touch "$2/loading"; while :; do sleep 1; done ;;
*) exit 1 ;;
esac
`)
	op := uuid.NewString()
	scratch := filepath.Join(e.RepositoriesRoot, ".verify-delete-"+op)
	dump := filepath.Join(t.TempDir(), "dump")
	if err := os.WriteFile(dump, []byte("test input"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := e.verifyDump(ctx, dump, op, func(context.Context, string, int64) error {
		if _, err := os.Stat(filepath.Join(scratch, "loading")); err == nil {
			return errDeletionCapacity
		}
		return nil
	})
	if !errors.Is(err, errDeletionCapacity) || ctx.Err() != nil || !validRepo(repo) {
		t.Fatalf("err=%v outer=%v source=%v", err, ctx.Err(), validRepo(repo))
	}
	if _, err := os.Stat(scratch); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scratch survived: %v", err)
	}
	if raw, err := os.ReadFile(dump); err != nil || string(raw) != "test input" {
		t.Fatalf("dump damaged: %q %v", raw, err)
	}
}

func TestDeletionCancellationStopsDescendantWrites(t *testing.T) {
	root := t.TempDir()
	heartbeat := filepath.Join(root, "heartbeat")
	script := deletionScript(t, `if [ "$1" = child ]; then
  trap '' TERM
  while :; do printf x >> "$2"; sleep .05; done
else
  "$0" child "$1" &
  wait
fi
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runDeletionCommand(ctx, root, func(context.Context, string, int64) error {
		if _, err := os.Stat(heartbeat); err == nil {
			return errDeletionCapacity
		}
		return nil
	}, script, []string{heartbeat}, func(*exec.Cmd, context.Context, context.CancelCauseFunc) {})
	if !errors.Is(err, errDeletionCapacity) || ctx.Err() != nil {
		t.Fatalf("err=%v outer=%v", err, ctx.Err())
	}
	before, err := os.Stat(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	after, err := os.Stat(heartbeat)
	if err != nil || before.Size() != after.Size() {
		t.Fatalf("descendant still writing: before=%v after=%v err=%v", before, after, err)
	}
}
