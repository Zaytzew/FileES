package clientview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type lockedUpdater struct {
	updates  int
	cleanups int
	view     string
	wc       string
	cleanErr error
	stayLock bool
}

func (u *lockedUpdater) Update(_ context.Context, path string) (string, error) {
	u.updates++
	if u.updates == 1 || u.stayLock {
		return "", errors.New("komenda 'update' zakończyła się błędem: exit status 1\n" +
			"svn: E155004: Run 'svn cleanup' to remove locks\n" +
			"svn: E155004: Working copy '" + path + "' locked.")
	}
	return "", os.WriteFile(filepath.Join(u.wc, "view.json"), []byte(u.view), 0o600)
}

func (u *lockedUpdater) Cleanup(context.Context, string) (string, error) {
	u.cleanups++
	return "", u.cleanErr
}

func lockedFixture(t *testing.T) (*lockedUpdater, SyncConfig) {
	t.Helper()
	root := t.TempDir()
	wc := filepath.Join(root, "service-wc")
	if err := os.MkdirAll(wc, 0o700); err != nil {
		t.Fatal(err)
	}
	view := `{"schema":"filees.client-view/v2","server_display_name":"Serwer testowy","client_id":"399c0801-46d2-4190-bd70-15a9bf6cfa00",` +
		`"realm_id":"a72d443d-342b-4ed8-9412-925247dbd4c5","generation":3,` +
		`"generated_at":"2026-09-03T16:47:00Z","client_role":"normal",` +
		`"repositories":[],"active_operations":[]}`
	return &lockedUpdater{view: view, wc: wc},
		SyncConfig{WorkingCopy: wc, RelativeViewPath: "view.json", CachePath: filepath.Join(root, "cache", "view.json")}
}

// The service working copy belongs to FileES, and a lock left by an interrupted
// operation is ours to clear. Subversion states the remedy in the error itself.
//
// Measured 2026-09-03: one server's service working copy stayed locked, every
// projection sync failed on it, and the interface reported that the server was
// not responding. The server was never contacted - the fault was entirely local
// - and the same cleanup-then-update that Checkout has always performed would
// have cleared it.
func TestALockedServiceCopyIsReleasedAndTheSyncProceeds(t *testing.T) {
	updater, config := lockedFixture(t)
	view, _, err := Sync(context.Background(), updater, config)
	if err != nil {
		t.Fatalf("a lock we can clear must not fail the sync: %v", err)
	}
	if updater.cleanups != 1 || updater.updates != 2 {
		t.Fatalf("cleanups=%d updates=%d; expected one cleanup and one retry", updater.cleanups, updater.updates)
	}
	if view.Generation != 3 {
		t.Fatalf("view = %+v", view)
	}
}

// Once. A lock surviving its own cleanup is a different fault and must be
// reported rather than retried around.
func TestALockThatSurvivesCleanupIsReported(t *testing.T) {
	updater, config := lockedFixture(t)
	updater.stayLock = true
	if _, _, err := Sync(context.Background(), updater, config); err == nil || !strings.Contains(err.Error(), "after cleanup") {
		t.Fatalf("a persistent lock must be reported as such: %v", err)
	}
	if updater.cleanups != 1 {
		t.Fatalf("cleanup must be attempted exactly once, got %d", updater.cleanups)
	}
}

// Anything that is not a lock keeps its own error. Cleanup must never become a
// blanket retry that hides transport or authorisation failures.
func TestOtherFailuresAreNotCleanedAround(t *testing.T) {
	root := t.TempDir()
	wc := filepath.Join(root, "service-wc")
	if err := os.MkdirAll(wc, 0o700); err != nil {
		t.Fatal(err)
	}
	updater := &refusingUpdater{}
	_, _, err := Sync(context.Background(), updater, SyncConfig{
		WorkingCopy: wc, RelativeViewPath: "view.json", CachePath: filepath.Join(root, "cache", "view.json"),
	})
	if err == nil || !strings.Contains(err.Error(), "proof does not match") {
		t.Fatalf("a refusal must survive unchanged: %v", err)
	}
	if updater.cleanups != 0 {
		t.Fatalf("nothing may be cleaned for a fault that is not a lock, got %d", updater.cleanups)
	}
}

type refusingUpdater struct{ cleanups int }

func (u *refusingUpdater) Update(context.Context, string) (string, error) {
	return "", errors.New("filees-client-entry proof: proof does not match one live staged or active client")
}
func (u *refusingUpdater) Cleanup(context.Context, string) (string, error) {
	u.cleanups++
	return "", nil
}
