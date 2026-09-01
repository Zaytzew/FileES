package reservationprojection

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	reservationv1 "filees/pkg/reservation/v1"
)

const repoA = "f5d5bfee-62f4-5b9c-b26f-8d4c424fb8f0"
const repoB = "1daf941a-c3da-53c3-9223-5c87a2c147a6"

func TestLoadOnEmptyDirIsUnknownNotError(t *testing.T) {
	s := NewStore(t.TempDir())
	art, ok, err := s.Load(repoA)
	if err != nil {
		t.Fatalf("unexpected error on first-ever load: %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false for a repository with no artifact yet, got %+v", art)
	}
}

func TestRefreshThenLoadRoundTrips(t *testing.T) {
	s := NewStore(t.TempDir())
	art, err := s.Refresh(repoA, func(prev Artifact, ok bool) ([]reservationv1.Reservation, error) {
		if ok {
			t.Fatalf("first ever refresh must see ok=false, got prev=%+v", prev)
		}
		return []reservationv1.Reservation{{Path: "a.txt", Token: "tok", OwnerID: "acme"}}, nil
	})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if art.Generation != 1 {
		t.Fatalf("first generation must be 1, got %d", art.Generation)
	}
	got, ok, err := s.Load(repoA)
	if err != nil || !ok {
		t.Fatalf("load after refresh: ok=%v err=%v", ok, err)
	}
	if got.Generation != 1 || len(got.Reservations) != 1 || got.Reservations[0].OwnerID != "acme" {
		t.Fatalf("round trip mismatch: got %+v", got)
	}
}

func TestRefreshIncrementsGenerationFromPreviousArtifact(t *testing.T) {
	s := NewStore(t.TempDir())
	if _, err := s.Refresh(repoA, func(Artifact, bool) ([]reservationv1.Reservation, error) { return nil, nil }); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	second, err := s.Refresh(repoA, func(prev Artifact, ok bool) ([]reservationv1.Reservation, error) {
		if !ok || prev.Generation != 1 {
			t.Fatalf("second refresh must see the first artifact as prev, got ok=%v prev=%+v", ok, prev)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if second.Generation != 2 {
		t.Fatalf("expected generation 2, got %d", second.Generation)
	}
}

func TestRefreshComputeErrorLeavesPriorArtifactUntouched(t *testing.T) {
	s := NewStore(t.TempDir())
	if _, err := s.Refresh(repoA, func(Artifact, bool) ([]reservationv1.Reservation, error) {
		return []reservationv1.Reservation{{Path: "a.txt"}}, nil
	}); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	_, err := s.Refresh(repoA, func(Artifact, bool) ([]reservationv1.Reservation, error) {
		return nil, errors.New("svn unreachable")
	})
	if err == nil {
		t.Fatal("expected the failing refresh to return an error")
	}
	art, ok, loadErr := s.Load(repoA)
	if loadErr != nil || !ok || art.Generation != 1 || len(art.Reservations) != 1 {
		t.Fatalf("a failed refresh must not touch the prior artifact: ok=%v err=%v art=%+v", ok, loadErr, art)
	}
}

// TestConcurrentRefreshesFromIndependentStoreInstancesNeverCollide is the
// real-world shape: every filees-serving-state invocation is its own OS
// process (internal/servertool/client_entry.go's ClientReservationCommand
// execs a fresh one per SSH session), so nothing in a Go-level in-memory
// mutex could ever be shared between two concurrent refreshes for the same
// repository — only a real, cross-process flock (Refresh's use of
// repoworker.WithFileLock) can serialize them. Using N independent *Store
// values, sharing nothing but the directory on disk, is what actually
// exercises that: a bug here would previously have been invisible to a
// single-Store, many-goroutines test, since goroutines within one process
// share the (removed) in-memory mutex regardless of whether it worked.
func TestConcurrentRefreshesFromIndependentStoreInstancesNeverCollide(t *testing.T) {
	dir := t.TempDir()
	const n = 20
	var wg sync.WaitGroup
	seen := make(chan int64, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store := NewStore(dir) // independent value: no shared Go state at all
			art, err := store.Refresh(repoA, func(Artifact, bool) ([]reservationv1.Reservation, error) { return nil, nil })
			if err != nil {
				t.Errorf("concurrent refresh: %v", err)
				return
			}
			seen <- art.Generation
		}()
	}
	wg.Wait()
	close(seen)
	generations := make(map[int64]bool, n)
	for g := range seen {
		if generations[g] {
			t.Fatalf("generation %d was published more than once", g)
		}
		generations[g] = true
	}
	if len(generations) != n {
		t.Fatalf("expected %d distinct generations, got %d", n, len(generations))
	}
	final, ok, err := NewStore(dir).Load(repoA)
	if err != nil || !ok || final.Generation != n {
		t.Fatalf("final artifact should be generation %d, got ok=%v err=%v art=%+v", n, ok, err, final)
	}
}

func TestRefreshIsAtomicNoTempFileSurvives(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if _, err := s.Refresh(repoA, func(Artifact, bool) ([]reservationv1.Reservation, error) { return nil, nil }); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temp file leaked after refresh: %s", e.Name())
		}
	}
}

func TestLoadRejectsCorruptArtifactInsteadOfPartialTrust(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := os.WriteFile(filepath.Join(dir, repoA+".json"), []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("write corrupt artifact: %v", err)
	}
	_, ok, err := s.Load(repoA)
	if err == nil {
		t.Fatal("expected an error for a corrupt artifact, got nil")
	}
	if ok {
		t.Fatal("corrupt artifact must never be reported as ok=true")
	}
}

func TestRefreshRecoversFromAPriorCorruptArtifact(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := os.WriteFile(filepath.Join(dir, repoA+".json"), []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("write corrupt artifact: %v", err)
	}
	art, err := s.Refresh(repoA, func(prev Artifact, ok bool) ([]reservationv1.Reservation, error) {
		if ok {
			t.Fatalf("a corrupt prior artifact must present as ok=false to compute, got %+v", prev)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("refresh after corruption: %v", err)
	}
	if art.Generation != 1 {
		t.Fatalf("recovery from corruption must restart generation at 1, got %d", art.Generation)
	}
}

func TestNonUUIDRepoIDsDoNotCollideAfterSanitization(t *testing.T) {
	// "a:b" and "a_b" would collide under a naive char-replacement sanitizer.
	dir := t.TempDir()
	s := NewStore(dir)
	if _, err := s.Refresh("a:b", func(Artifact, bool) ([]reservationv1.Reservation, error) { return nil, nil }); err != nil {
		t.Fatalf("refresh a:b: %v", err)
	}
	if _, err := s.Refresh("a_b", func(Artifact, bool) ([]reservationv1.Reservation, error) { return nil, nil }); err != nil {
		t.Fatalf("refresh a_b: %v", err)
	}
	first, ok, err := s.Load("a:b")
	if err != nil || !ok || first.RepoID != "a:b" {
		t.Fatalf("a:b artifact wrong: ok=%v err=%v got=%+v", ok, err, first)
	}
	second, ok, err := s.Load("a_b")
	if err != nil || !ok || second.RepoID != "a_b" {
		t.Fatalf("a_b artifact wrong: ok=%v err=%v got=%+v", ok, err, second)
	}
}

func TestMultipleRepositoriesDoNotCollide(t *testing.T) {
	s := NewStore(t.TempDir())
	if _, err := s.Refresh(repoA, func(Artifact, bool) ([]reservationv1.Reservation, error) {
		return []reservationv1.Reservation{{Path: "a.txt"}}, nil
	}); err != nil {
		t.Fatalf("refresh repoA: %v", err)
	}
	if _, err := s.Refresh(repoB, func(Artifact, bool) ([]reservationv1.Reservation, error) { return nil, nil }); err != nil {
		t.Fatalf("refresh repoB: %v", err)
	}
	a, ok, err := s.Load(repoA)
	if err != nil || !ok || len(a.Reservations) != 1 {
		t.Fatalf("repoA artifact wrong: ok=%v err=%v got=%+v", ok, err, a)
	}
	b, ok, err := s.Load(repoB)
	if err != nil || !ok || len(b.Reservations) != 0 {
		t.Fatalf("repoB artifact wrong: ok=%v err=%v got=%+v", ok, err, b)
	}
}

func TestSurvivesProcessRestartSimulation(t *testing.T) {
	dir := t.TempDir()
	first := NewStore(dir)
	if _, err := first.Refresh(repoA, func(Artifact, bool) ([]reservationv1.Reservation, error) { return nil, nil }); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := first.Refresh(repoA, func(Artifact, bool) ([]reservationv1.Reservation, error) { return nil, nil }); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	// A fresh Store value simulates a worker process restart: no in-memory
	// state survives, only the directory on disk.
	second := NewStore(dir)
	art, ok, err := second.Load(repoA)
	if err != nil || !ok || art.Generation != 2 {
		t.Fatalf("artifact did not survive restart: ok=%v err=%v got=%+v", ok, err, art)
	}
}
