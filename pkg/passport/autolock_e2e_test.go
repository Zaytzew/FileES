package passport

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/client"
)

// runAutolockCmd runs an external command and fails the test with combined
// output on error - shared shell-out helper for this file's SVN plumbing.
func runAutolockCmd(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// newAutolockE2ERepo creates a real svnadmin repository with one file that
// already carries svn:needs-lock (mirroring EDIT_PASSPORTS.md's migration
// outcome, not the pre-migration state) and returns the first working copy.
func newAutolockE2ERepo(t *testing.T) string {
	t.Helper()
	for _, bin := range []string{"svnadmin", "svn"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH — skipping autolock E2E test", bin)
		}
	}
	base := t.TempDir()
	repoPath := filepath.Join(base, "repo")
	wc := filepath.Join(base, "wc-origin")
	runAutolockCmd(t, "svnadmin", "create", repoPath)
	repoURL := "file://" + repoPath
	runAutolockCmd(t, "svn", "checkout", "--non-interactive", "--no-auth-cache", repoURL, wc)
	doc := filepath.Join(wc, "doc.txt")
	if err := os.WriteFile(doc, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	runAutolockCmd(t, "svn", "add", "--non-interactive", "--no-auth-cache", doc)
	runAutolockCmd(t, "svn", "propset", "svn:needs-lock", "*", doc)
	runAutolockCmd(t, "svn", "commit", "--non-interactive", "--no-auth-cache", "-m", "init", wc)
	return wc
}

// checkoutAutolockWC checks out a second, independent working copy of the
// same repository - simulating another machine (same or different realm).
func checkoutAutolockWC(t *testing.T, originWC string) string {
	t.Helper()
	repoURL := strings.TrimSpace(runAutolockCmd(t, "svn", "info", "--show-item", "url", originWC))
	wc := filepath.Join(t.TempDir(), "wc")
	runAutolockCmd(t, "svn", "checkout", "--non-interactive", "--no-auth-cache", repoURL, wc)
	return wc
}

// TestAutolockE2ERealSVN is the live-SVN end-to-end verification of
// AUTOLOCK_CREATOR_OWNERSHIP_CONCEPT_V2.md's V1 mechanics: a real svnadmin
// repository, real svn:needs-lock, three real checkouts, and the real
// Manager.AutoUnlockOwned/Acquire (no fakeBackend anywhere in this test) -
// covering every scenario the concept doc names as load-bearing:
//  1. the owner realm never touches a manual lock action;
//  2. the real SVN lock is only requested at publish time, never at checkout;
//  3. a second instance of the SAME realm migrates the lock silently;
//  4. a foreign realm gets no autolock at all (today's manual flow, unchanged);
//  5. the owner realm never steals a foreign realm's real lock, and a local
//     edit made before that foreign lock existed is never lost - only held
//     back until the foreign realm releases.
func TestAutolockE2ERealSVN(t *testing.T) {
	origin := newAutolockE2ERepo(t)
	wcA1 := checkoutAutolockWC(t, origin) // realm-a, instance "laptop"
	wcA2 := checkoutAutolockWC(t, origin) // realm-a, instance "desktop"
	wcB := checkoutAutolockWC(t, origin)  // realm-b, foreign

	now := time.Now()
	mA1 := newSVNManager(t, wcA1, &now, Config{})
	mA2 := newSVNManager(t, wcA2, &now, Config{})
	mB := newSVNManager(t, wcB, &now, Config{})
	ctx := context.Background()
	cli := client.New(client.Options{Timeout: 10 * time.Second})

	docA1 := filepath.Join(wcA1, "doc.txt")
	docA2 := filepath.Join(wcA2, "doc.txt")
	docB := filepath.Join(wcB, "doc.txt")

	// 1. Fresh checkout: SVN's own svn:needs-lock behaviour made the file
	// read-only, identically for every checkout, before anything FileES
	// does. This is the baseline the rest of the test builds on.
	assertWritable(t, docA1, false)
	assertWritable(t, docB, false)

	// 2. AutoUnlockOwned grants local RW for the owner realm - with zero
	// real SVN lock yet. This is the core claim that separates "local RW"
	// from "server exclusivity" in the concept doc.
	if err := mA1.AutoUnlockOwned(ctx, wcA1, "realm-a"); err != nil {
		t.Fatal(err)
	}
	assertWritable(t, docA1, true)
	if info, err := cli.LockInfo(ctx, wcA1, docA1); err != nil || info != nil {
		t.Fatalf("real SVN lock exists before any publish attempt: info=%v err=%v", info, err)
	}

	// 3. Edit, then "publish" - mirrors buildCommitService's owner wrapper:
	// real, non-destructive Acquire happens only now, immediately before
	// the real svn commit.
	if err := os.WriteFile(docA1, []byte("edited by realm-a laptop"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mA1.Acquire(ctx, []string{docA1}, "realm-a"); err != nil {
		t.Fatalf("owner realm Acquire at publish time: %v", err)
	}
	runAutolockCmd(t, "svn", "commit", "--non-interactive", "--no-auth-cache", "--no-unlock", "-m", "edit from laptop", wcA1)

	// 4. Second instance of the SAME realm: after svn up, AutoUnlockOwned
	// grants local RW again (fresh RO from the update), and Acquire at
	// publish time migrates the real lock silently - no dialog, no error,
	// even though laptop's lock has not expired.
	runAutolockCmd(t, "svn", "update", "--non-interactive", "--no-auth-cache", wcA2)
	if err := mA2.AutoUnlockOwned(ctx, wcA2, "realm-a"); err != nil {
		t.Fatal(err)
	}
	assertWritable(t, docA2, true)
	if err := os.WriteFile(docA2, []byte("edited by realm-a desktop"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mA2.Acquire(ctx, []string{docA2}, "realm-a"); err != nil {
		t.Fatalf("same-realm silent migration at publish time: %v", err)
	}
	runAutolockCmd(t, "svn", "commit", "--non-interactive", "--no-auth-cache", "--no-unlock", "-m", "edit from desktop", wcA2)
	// Simulate the quiet-grace period completing (EDIT_PASSPORTS.md's
	// close-grace unlock, out of scope for this test) so the path is
	// genuinely free before the race scenario in step 6.
	if _, err := mA2.Release(ctx, []string{docA2}); err != nil {
		t.Fatal(err)
	}

	// 5. Foreign realm uses the manual "Wypożycz do edycji" path - a plain
	// Acquire, exactly like today. (AutoUnlockOwned itself is realm-agnostic
	// by design: it acts on whatever realmID it's given, for any free-or-
	// same-realm path. The exclusion of non-owner realms from ever being
	// handed that call at all is enforced by the caller - pkg/commit's
	// pollOnce only invokes it when RealmID == OwnerRealmID - not by this
	// package, so it is out of scope for this SVN-level test.)
	runAutolockCmd(t, "svn", "update", "--non-interactive", "--no-auth-cache", wcB)
	assertWritable(t, docB, false)

	// 6. The real race the whole design exists to handle safely: the owner
	// realm gets local RW and edits BEFORE the foreign realm's manual
	// borrow exists yet, then the foreign realm borrows first. The owner's
	// publish attempt must fail (not steal), and the owner's local edit
	// must survive untouched, ready for a later retry.
	runAutolockCmd(t, "svn", "update", "--non-interactive", "--no-auth-cache", wcA1)
	if err := mA1.AutoUnlockOwned(ctx, wcA1, "realm-a"); err != nil {
		t.Fatal(err)
	}
	assertWritable(t, docA1, true)
	const raceEdit = "realm-a's in-flight edit, must survive"
	if err := os.WriteFile(docA1, []byte(raceEdit), 0o644); err != nil {
		t.Fatal(err)
	}

	// Foreign realm wins the race for the real lock via its own manual
	// borrow (mirrors "Wypożycz do edycji" - realmID irrelevant to a
	// straight Acquire call on an unheld path, but stamped for realism).
	if _, _, err := mB.Acquire(ctx, []string{docB}, "realm-b"); err != nil {
		t.Fatalf("foreign realm's own manual borrow: %v", err)
	}

	// Owner realm's publish attempt must now fail - never steal.
	if _, _, err := mA1.Acquire(ctx, []string{docA1}, "realm-a"); err == nil {
		t.Fatal("owner realm silently stole a foreign realm's real lock")
	}
	// The real SVN lock is still the foreign realm's.
	info, err := cli.LockInfo(ctx, wcA1, docA1)
	if err != nil || info == nil {
		t.Fatalf("expected the foreign realm's real lock to still be held: info=%v err=%v", info, err)
	}
	// The local edit was never touched by the failed Acquire attempt.
	raw, err := os.ReadFile(docA1)
	if err != nil || string(raw) != raceEdit {
		t.Fatalf("owner's local edit was lost or altered after a blocked publish: content=%q err=%v", raw, err)
	}

	// Once the foreign realm releases, the owner's retry succeeds and its
	// original edit is exactly what gets published - nothing was dropped
	// during the hold-back window.
	if _, err := mB.Release(ctx, []string{docB}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mA1.Acquire(ctx, []string{docA1}, "realm-a"); err != nil {
		t.Fatalf("owner realm retry after foreign release: %v", err)
	}
	runAutolockCmd(t, "svn", "commit", "--non-interactive", "--no-auth-cache", "--no-unlock", "-m", "retry after foreign release", wcA1)
}
