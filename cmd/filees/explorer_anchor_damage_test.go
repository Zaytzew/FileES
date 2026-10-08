//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"filees/internal/anchorrepair"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

func TestAnchorDamageIsReportedCountedAndClearedAfterRepair(t *testing.T) {
	original := anchorDamageScan
	t.Cleanup(func() { anchorDamageScan = original })
	record := localrepo.Record{ServerID: "office", RepoID: "repo", LocalPath: `F:\work\anchor`}
	key := anchorKey(record)
	var announced []string
	m := &anchorManager{guardChanged: func(repoID string) { announced = append(announced, repoID) }}

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
	// The GUI is told when the answer changes and only then: found, cleared.
	if m.inspectDamage(record); len(announced) != 2 || announced[0] != "repo" {
		t.Fatalf("state change announcements: %q", announced)
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

// At daemon start an anchor connects before its repository state is
// registered. The damage found then must still reach the GUI once the state
// exists: this is what the owner's station showed on 2026-10-08.
func TestAnchorDamageReachesARepositoryStateRegisteredAfterTheFirstInspection(t *testing.T) {
	original := anchorDamageScan
	t.Cleanup(func() { anchorDamageScan = original })
	anchorDamageScan = func(string) ([]anchorrepair.Damaged, error) {
		return []anchorrepair.Damaged{{Path: `Archiwum Robocze\a.pdf`}, {Path: `Archiwum Robocze\b.pdf`}}, nil
	}
	server := ipcserver.New(filepath.Join(t.TempDir(), "daemon.sock"))
	record := localrepo.Record{ServerID: "office", RepoID: "repo", LocalPath: t.TempDir()}
	var registered *ipcserver.RepoState
	var announced int
	m := &anchorManager{
		repos:        func(string, string) *ipcserver.RepoState { return registered },
		guardChanged: func(string) { announced++ },
	}
	if got := m.inspectDamage(record); got != 2 || announced != 1 {
		t.Fatalf("first inspection: %d entries, %d announcements", got, announced)
	}
	registered = server.RegisterRepoAccess(record.RepoID, "svn+ssh://example.invalid/repo", record.LocalPath, record.ServerID, "rw")
	if got := m.inspectDamage(record); got != 2 || announced != 2 {
		t.Fatalf("inspection after registration: %d entries, %d announcements", got, announced)
	}
	if damage := registered.Snapshot().AnchorDamage; damage == nil || damage.Entries != 2 {
		t.Fatalf("snapshot does not carry the damage: %+v", damage)
	}
	// Nothing new: no further announcement.
	if m.inspectDamage(record); announced != 2 {
		t.Fatalf("unchanged state announced again: %d", announced)
	}
}
