//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"filees/internal/anchorrepair"
	"filees/pkg/localrepo"
)

func TestAnchorDamageIsReportedCountedAndClearedAfterRepair(t *testing.T) {
	original := anchorDamageScan
	t.Cleanup(func() { anchorDamageScan = original })
	record := localrepo.Record{ServerID: "office", RepoID: "repo", LocalPath: `F:\work\anchor`}
	key := anchorKey(record)
	m := &anchorManager{}

	var found []anchorrepair.Damaged
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		found = append(found, anchorrepair.Damaged{Path: `02\` + name + ".pdf"})
	}
	anchorDamageScan = func(root string) ([]anchorrepair.Damaged, error) {
		if root != record.LocalPath {
			t.Fatalf("inspected %q", root)
		}
		return found, nil
	}
	if m.damageOf(key) != nil {
		t.Fatal("damage before any inspection")
	}
	if got := m.inspectDamage(record); got != 7 {
		t.Fatalf("inspect: %d", got)
	}
	damage := m.damageOf(key)
	if damage == nil || damage.Entries != 7 || len(damage.Sample) != anchorDamageSample || damage.Sample[0] != `02\a.pdf` {
		t.Fatalf("damage: %+v", damage)
	}
	// The GUI gets a copy: changing it cannot change what the daemon knows.
	damage.Sample[0] = "changed"
	if again := m.damageOf(key); again.Sample[0] != `02\a.pdf` {
		t.Fatalf("shared sample: %+v", again)
	}

	// A folder that cannot be inspected right now keeps its last answer.
	anchorDamageScan = func(string) ([]anchorrepair.Damaged, error) { return nil, errors.New("filter detached") }
	if got := m.inspectDamage(record); got != 7 || m.damageOf(key) == nil {
		t.Fatalf("unreadable folder changed the answer: %d %+v", got, m.damageOf(key))
	}

	anchorDamageScan = func(string) ([]anchorrepair.Damaged, error) { return nil, nil }
	if got := m.inspectDamage(record); got != 0 || m.damageOf(key) != nil {
		t.Fatalf("repaired folder still reported: %d %+v", got, m.damageOf(key))
	}
}

func TestAnchorDamageWatchLooksAgainSoonOnlyWhileSomethingIsDamaged(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	answers := []int{3, 3, 0}
	var calls atomic.Int32
	var gaps []time.Duration
	last := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchAnchorDamage(ctx, 20*time.Millisecond, time.Hour, func() int {
			index := int(calls.Add(1)) - 1
			gaps = append(gaps, time.Since(last))
			last = time.Now()
			if index >= len(answers)-1 {
				cancel()
				return answers[len(answers)-1]
			}
			return answers[index]
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not end")
	}
	if calls.Load() != 3 {
		t.Fatalf("inspections: %d (gaps %v)", calls.Load(), gaps)
	}
	if !reflect.DeepEqual(len(gaps), 3) || gaps[0] > 15*time.Millisecond {
		t.Fatalf("first inspection was not immediate: %v", gaps)
	}
}
