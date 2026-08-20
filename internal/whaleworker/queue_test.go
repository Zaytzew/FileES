package whaleworker

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestPathQueueIsPersistentFIFOAndGenerationRetryIsIdempotent(t *testing.T) {
	queue := PathQueue{Root: filepath.Join(t.TempDir(), "queues")}
	first := identity()
	second, third := identity(), identity()
	second.LogicalRepoID, second.LogicalPath = first.LogicalRepoID, first.LogicalPath
	third.LogicalRepoID, third.LogicalPath = first.LogicalRepoID, first.LogicalPath
	if position, err := queue.Claim(first); err != nil || position != 0 {
		t.Fatalf("first position=%d err=%v", position, err)
	}
	if position, err := queue.Claim(first); err != nil || position != 0 {
		t.Fatalf("first retry position=%d err=%v", position, err)
	}
	if position, err := queue.Claim(second); err != nil || position != 1 {
		t.Fatalf("second position=%d err=%v", position, err)
	}
	if position, err := queue.Claim(third); err != nil || position != 2 {
		t.Fatalf("third position=%d err=%v", position, err)
	}
	if err := queue.Release(first); err != nil {
		t.Fatal(err)
	}
	if position, err := queue.Claim(second); err != nil || position != 0 {
		t.Fatalf("promoted second position=%d err=%v", position, err)
	}
	if err := queue.Release(second); err != nil {
		t.Fatal(err)
	}
	if position, err := queue.Claim(third); err != nil || position != 0 {
		t.Fatalf("promoted third position=%d err=%v", position, err)
	}
}

func TestOnlyHolderCanReleasePath(t *testing.T) {
	queue := PathQueue{Root: filepath.Join(t.TempDir(), "queues")}
	first, second := identity(), identity()
	second.LogicalRepoID, second.LogicalPath = first.LogicalRepoID, first.LogicalPath
	_, _ = queue.Claim(first)
	_, _ = queue.Claim(second)
	if err := queue.Release(second); err == nil {
		t.Fatal("waiter released holder")
	}
}

func TestBusyErrorCanCarryDurableFIFOPosition(t *testing.T) {
	err := error(BusyError{Position: 3})
	var busy BusyError
	if !errors.As(err, &busy) || busy.Position != 3 {
		t.Fatalf("busy=%+v", busy)
	}
}
