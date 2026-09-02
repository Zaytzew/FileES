package servertool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestQueryLiveLocksAgainstARealRepository exercises the actual subprocess
// (svn info -r HEAD --xml --depth infinity -- file://...@) against a real,
// disposable svnadmin repository — not a fake. This is the one place in
// this package's reservation-projection tests that is not a pure-function
// test; it is the empirical proof behind
// concepts/RESERVATION_SERVER_EMISSION_WORKPLAN.md's design (verified
// manually before this test existed, then pinned here so it cannot regress
// silently).
func TestQueryLiveLocksAgainstARealRepository(t *testing.T) {
	tools := requireSVN(t, "svnadmin", "svn")
	svnadmin, svn := tools[0], tools[1]

	root := t.TempDir()
	// A space in the path exercises url.URL-based file:// construction —
	// a naive "file://"+path string concatenation breaks svn's own URL
	// parsing here.
	repoPath := filepath.Join(root, "my repo")
	if out, err := exec.Command(svnadmin, "create", repoPath).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, out)
	}
	wc := filepath.Join(root, "wc")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(svn, args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("svn %v: %v: %s", args, err, out)
		}
	}
	run("checkout", "file://"+repoPath, wc, "-q")
	if err := os.WriteFile(filepath.Join(wc, "a.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(wc, "sub"), 0755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wc, "sub", "b.txt"), []byte("world\n"), 0644); err != nil {
		t.Fatalf("write sub/b.txt: %v", err)
	}
	addCmd := exec.Command(svn, "add", "a.txt", "sub", "-q")
	addCmd.Dir = wc
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("svn add: %v: %s", err, out)
	}
	commitCmd := exec.Command(svn, "commit", "-m", "init", "-q")
	commitCmd.Dir = wc
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("svn commit: %v: %s", err, out)
	}
	lockCmd := exec.Command(svn, "lock", "a.txt", "-m", "editing a", "-q")
	lockCmd.Dir = wc
	if out, err := lockCmd.CombinedOutput(); err != nil {
		t.Fatalf("svn lock: %v: %s", err, out)
	}

	reservations, err := queryLiveLocks(context.Background(), svn, repoPath)
	if err != nil {
		t.Fatalf("queryLiveLocks: %v", err)
	}
	if len(reservations) != 1 {
		t.Fatalf("expected exactly 1 lock, got %+v", reservations)
	}
	if reservations[0].Path != "a.txt" || reservations[0].Comment != "editing a" || reservations[0].Token == "" || reservations[0].OwnerID == "" || reservations[0].CreatedAt == "" {
		t.Fatalf("lock fields wrong: %+v", reservations[0])
	}

	// Release the lock and confirm the next live query reports zero — a
	// confirmed empty list, not an error.
	unlockCmd := exec.Command(svn, "unlock", "a.txt", "-q")
	unlockCmd.Dir = wc
	if out, err := unlockCmd.CombinedOutput(); err != nil {
		t.Fatalf("svn unlock: %v: %s", err, out)
	}
	reservations, err = queryLiveLocks(context.Background(), svn, repoPath)
	if err != nil {
		t.Fatalf("queryLiveLocks after unlock: %v", err)
	}
	if len(reservations) != 0 {
		t.Fatalf("expected zero locks after unlock, got %+v", reservations)
	}
}
