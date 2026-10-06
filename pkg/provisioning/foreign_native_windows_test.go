//go:build windows && native_foreign_probe

package provisioning

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filees/pkg/cloudfiles"
	"filees/pkg/errcat"
	"github.com/google/uuid"
)

// The independent fixture registers a synthetic provider but never connects
// it. Run also with nocfapi: no FileES helper, hydration or user folders.
func foreignFixture(t *testing.T) (string, string) {
	t.Helper()
	helper := os.Getenv("FILEES_LAB_FOREIGN_HELPER")
	if helper == "" {
		t.Fatal("FILEES_LAB_FOREIGN_HELPER is required for native_foreign_probe")
	}
	root := t.TempDir()
	if out, err := exec.Command(helper, "create", root).CombinedOutput(); err != nil {
		t.Fatalf("create independent fixture: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(helper, "remove", root).CombinedOutput(); err != nil {
			t.Errorf("unregister fixture: %v: %s", err, out)
		}
	})
	path := filepath.Join(root, "unavailable.dwg")
	info, err := os.Lstat(path)
	if err != nil || info.Size() != 4096 || !cloudfiles.RecallsOnRead(info) {
		t.Fatalf("missing real unavailable fixture: info=%v err=%v", info, err)
	}
	return root, path
}

func requireForeignRefusal(t *testing.T, err error) {
	t.Helper()
	var fault errcat.Fault
	if !errors.As(err, &fault) || fault.Key != errcat.KeyInitialContentUnavailable || fault.Details["detail"] == "" {
		t.Fatalf("expected actionable provider refusal, got %v", err)
	}
}

func TestForeignNativeInitialImportRefusesBeforeCheckoutAndResumes(t *testing.T) {
	root, path := foreignFixture(t)
	snapshot, err := ScanInitialSnapshot(root)
	requireForeignRefusal(t, err)
	if len(snapshot.Files) != 0 || snapshot.TotalBytes != 0 {
		t.Fatalf("refused snapshot escaped as partial import: %+v", snapshot)
	}
	store := readyOperation(t, root)
	id := onlyOperationID(t, store)
	before, _ := store.Get(id)
	svn := &fakeInitialSVN{root: root, items: map[string]string{}}
	limits := ImportLimits{MaxBatchFiles: 10, MaxBatchBytes: 1024}
	_, err = PublishInitialSnapshot(context.Background(), store, id, uuid.NewString(), svn, limits)
	requireForeignRefusal(t, err)
	failed, _ := store.Get(id)
	if failed.State != StateInitialCommitFailed || failed.RepoID != before.RepoID || len(svn.commits) != 0 {
		t.Fatalf("unsafe import boundary: %+v commits=%v", failed, svn.commits)
	}
	if _, err := os.Lstat(filepath.Join(root, ".svn")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout ran before refusal: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !cloudfiles.RecallsOnRead(info) || info.Size() != 4096 {
		t.Fatalf("refusal changed provider file: %v %v", info, err)
	}
	// Synthetic fixture only: stand in for data recovered by its provider.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("recovered fixture bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := PublishInitialSnapshot(context.Background(), store, id, uuid.NewString(), svn, limits)
	if err != nil || after.OperationID != before.OperationID || after.RepoID != before.RepoID || len(svn.commits) != 1 {
		t.Fatalf("retry lost identity: %+v %v commits=%v", after, err, svn.commits)
	}
}

func TestForeignNativeQueuedCreateMakesNoRemoteRequest(t *testing.T) {
	root, _ := foreignFixture(t)
	store := newTestStore(t, filepath.Join(t.TempDir(), "state"))
	id := uuid.NewString()
	if _, err := store.CreateValidated(id, "fixture", root, "Fixture"); err != nil {
		t.Fatal(err)
	}
	transport := &fakeControlExchange{}
	svn := &fakeInitialSVN{root: root, items: map[string]string{}}
	o := Orchestrator{Store: store, Control: transport, SVN: svn, Limits: ImportLimits{MaxBatchFiles: 10, MaxBatchBytes: 1024}}
	_, err := o.RunCreate(context.Background(), id)
	requireForeignRefusal(t, err)
	if len(transport.tickets) != 0 || len(svn.commits) != 0 {
		t.Fatalf("provider refusal performed remote work: %v %v", transport.tickets, svn.commits)
	}
}

type dehydratingInitialSVN struct {
	*fakeInitialSVN
	onAdd func()
}

func (s *dehydratingInitialSVN) Add(ctx context.Context, root string, paths []string) (string, error) {
	out, err := s.fakeInitialSVN.Add(ctx, root, paths)
	s.onAdd()
	return out, err
}

func TestForeignNativeAvailabilityRecheckedBeforeCommit(t *testing.T) {
	root, localPath := foreignFixture(t)
	if err := os.Remove(localPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localPath, []byte("real local bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := readyOperation(t, root)
	svn := &dehydratingInitialSVN{fakeInitialSVN: &fakeInitialSVN{root: root, items: map[string]string{}}, onAdd: func() {
		if err := os.Remove(localPath); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(os.Getenv("FILEES_LAB_FOREIGN_HELPER"), "create", root).CombinedOutput(); err != nil {
			t.Fatalf("dehydrate synthetic fixture: %v: %s", err, out)
		}
	}}
	_, err := PublishInitialSnapshot(context.Background(), store, onlyOperationID(t, store), uuid.NewString(), svn, ImportLimits{MaxBatchFiles: 10, MaxBatchBytes: 1024})
	requireForeignRefusal(t, err)
	if len(svn.commits) != 0 {
		t.Fatalf("dehydrated file committed: %v", svn.commits)
	}
}
