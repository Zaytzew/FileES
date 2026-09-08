package main

import (
	"context"
	"errors"
	"filees/internal/svnurl"
	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/reposupervisor"
	reservationv1 "filees/pkg/reservation/v1"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRemoteDeletionInspectsTwoRealSVNCopiesWithoutChangingFiles(t *testing.T) {
	for _, bin := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " unavailable")
		}
	}
	run := func(name string, args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(t.Context(), name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v: %s", name, args, err, out)
		}
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
	run("svnadmin", "create", repo)
	repoURL := svnurl.File(repo)
	if runtime.GOOS == "windows" {
		repoURL = "file:///" + filepath.ToSlash(repo)
	}
	run("svn", "checkout", repoURL, a)
	tracked := filepath.Join(a, "document.txt")
	if err := os.WriteFile(tracked, []byte("committed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("svn", "add", tracked)
	run("svn", "commit", "-m", "fixture", a)
	run("svn", "checkout", repoURL, b)
	for _, wc := range []string{a, b} {
		if err := os.MkdirAll(filepath.Join(wc, ".filees", "state"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"document.txt", "untracked.txt"} {
		if err := os.WriteFile(filepath.Join(b, name), []byte("unsent\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i, wc := range []string{a, b, filepath.Join(root, "missing")} {
		id := []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}[i]
		r, _, err := store.EnsureConfiguredAttached("lab", id, "svn+ssh://lab/docs", "rw", wc, "Docs")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ObserveRemoteDeletion("lab", id); err != nil {
			t.Fatal(err)
		}
		if err := inspectPreservedCopies(t.Context(), store, reposupervisor.Key{ServerID: "lab", RepoID: id}); err != nil {
			t.Fatal(err)
		}
		want := []string{"clean", "changed", "unknown"}[i]
		for _, observed := range store.List() {
			if observed.OperationID == r.OperationID && observed.PreservedCopyStatus != want {
				t.Fatalf("copy %d status=%s want=%s", i, observed.PreservedCopyStatus, want)
			}
		}
		if err := cleanupRemoteDeletedCopies(t.Context(), store, reposupervisor.Key{ServerID: "lab", RepoID: id}); err != nil {
			t.Fatal(err)
		}
		got, _ := store.Get(r.OperationID)
		if !got.LocalCleanupCompleted || !got.RemoteCleanupStarted || got.PreservedCopyStatus != want {
			t.Fatalf("cleanup receipt: %+v", got)
		}
		service := repositoryLifecycleService{store: store, onDetach: func(context.Context, string) (localrepo.Record, error) {
			t.Fatal("completed orphan called active detach executor")
			return localrepo.Record{}, nil
		}}
		if _, err := service.BeginDetach(t.Context(), "lab", id, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"document.txt", "untracked.txt"} {
		data, err := os.ReadFile(filepath.Join(b, name))
		if err != nil || string(data) != "unsent\n" {
			t.Fatalf("local work changed: %q %v", data, err)
		}
	}
	for _, wc := range []string{a, b} {
		for _, metadata := range []string{".svn", ".filees"} {
			if _, err := os.Lstat(filepath.Join(wc, metadata)); !os.IsNotExist(err) {
				t.Fatalf("metadata survived: %s %v", metadata, err)
			}
		}
	}
	store, err = localrepo.Open(filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	ipc := ipcserver.New(filepath.Join(root, "ipc.sock"))
	for _, record := range store.List() {
		if !record.LocalProjectionDismissed || !store.RemoteDeleted(record.ServerID, record.RepoID) {
			t.Fatal("lost acknowledgement or fence")
		}
		stale := clientview.View{Generation: 1, Repositories: []clientview.Repository{{RepoID: record.RepoID, State: "active"}}}
		syncProjectionKnowledge(ipc, record.ServerID, stale, map[reposupervisor.Key]repoRuntime{}, store)
		if ipc.RepoState(record.ServerID, record.RepoID) != nil {
			t.Fatal("stale view resurrected dismissed projection")
		}
	}
	// A completed receipt must not touch a folder handed back to its owner.
	if err := os.Mkdir(filepath.Join(a, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := cleanupRemoteDeletedCopies(t.Context(), store, reposupervisor.Key{ServerID: "lab", RepoID: "11111111-1111-4111-8111-111111111111"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a, ".svn")); err != nil {
		t.Fatal("replayed cleanup on already released folder", err)
	}
}

type terminalFetcher struct {
	fixedReservationFetcher
	state    reservationv1.Result
	stateErr error
	calls    int
}

type cancellingAttachment struct {
	attachmentSVNStub
	started chan struct{}
}

func (s *cancellingAttachment) Checkout(ctx context.Context, _, _ string) (string, error) {
	close(s.started)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestRemoteDeletionCancelsInFlightAttachmentAndFencesQueue(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	id := "11111111-1111-4111-8111-111111111111"
	rec, err := store.BeginAttach("lab", id, filepath.Join(t.TempDir(), "wc"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveAttach(rec.OperationID, "lab", id, "svn+ssh://_filees-client@lab/"+id, "rw"); err != nil {
		t.Fatal(err)
	}
	p := newDaemonProvisioner(store, nil, []clientprofile.Profile{{ServerID: "lab"}})
	stub := &cancellingAttachment{started: make(chan struct{})}
	p.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN { return stub }
	done := make(chan struct{})
	go func() { defer close(done); p.runOne(t.Context(), rec.OperationID) }()
	select {
	case <-stub.started:
	case <-time.After(5 * time.Second):
		t.Fatal("attach did not start")
	}
	if err := store.ObserveRemoteDeletion("lab", id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := p.StopRepository(ctx, "lab", id); err != nil {
		t.Fatal(err)
	}
	<-done
	p.runOne(t.Context(), rec.OperationID) // must not enter Checkout again
	current, _ := store.Get(rec.OperationID)
	if !current.RemoteDeletionObserved || current.State != localrepo.StateDetached {
		t.Fatalf("late completion: %+v", current)
	}
}

func (f *terminalFetcher) FetchState(context.Context, string) (reservationv1.Result, error) {
	f.calls++
	return f.state, f.stateErr
}
func TestRemoteDeletionOfflineCloneRestartAndStaleView(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "lifecycle.json")
	store, err := localrepo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := "11111111-1111-4111-8111-111111111111"
	rec, _, err := store.EnsureConfiguredAttached("lab", id, "svn+ssh://lab/docs", "rw", filepath.Join(t.TempDir(), "Docs"), "Docs")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	f := &terminalFetcher{state: reservationv1.Result{Schema: reservationv1.StateSchema, RepoID: id, RepositoryState: "deleted", ViewGeneration: 8, ViewGeneratedAt: &now}, stateErr: errors.New("offline")}
	c := newReservationProjectionCoordinator(ctx, nil)
	defer c.Close()
	c.lifecycle = store
	c.profiles["lab"] = clientprofile.Profile{ServerID: "lab"}
	c.views["lab"] = clientview.View{Generation: 8}
	c.newClient = func(clientprofile.Profile) (reservationFetcher, error) { return f, nil }
	applied := 0
	c.onRepositoryDeleted = func(_ context.Context, k reposupervisor.Key) error {
		applied++
		return store.ObserveRemoteDeletion(k.ServerID, k.RepoID)
	}
	c.refresh(ctx, "lab")
	if store.RemoteDeleted("lab", id) || applied != 0 {
		t.Fatal("offline inferred deletion")
	}
	f.stateErr = nil
	c.refresh(ctx, "lab")
	if !store.RemoteDeleted("lab", id) || applied != 1 {
		t.Fatal("absent local repo not reconciled")
	}
	c.refresh(ctx, "lab")
	if applied != 1 {
		t.Fatal("receipt not idempotent")
	}
	// Simulate crash after persistence, before runtime application.
	restarted, err := localrepo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c2 := newReservationProjectionCoordinator(ctx, nil)
	defer c2.Close()
	c2.lifecycle = restarted
	c2.profiles["lab"] = clientprofile.Profile{ServerID: "lab"}
	stale := clientview.View{Generation: 1, Repositories: []clientview.Repository{{RepoID: id, State: "active"}}}
	c2.views["lab"] = stale
	c2.newClient = c.newClient
	resumed := 0
	c2.onRepositoryDeleted = func(_ context.Context, k reposupervisor.Key) error {
		resumed++
		return restarted.ObserveRemoteDeletion(k.ServerID, k.RepoID)
	}
	calls := f.calls
	c2.refresh(ctx, "lab")
	if resumed != 1 || f.calls != calls {
		t.Fatal("restart did not replay local receipt without network")
	}
	ipc := ipcserver.New(filepath.Join(t.TempDir(), "ipc.sock"))
	syncProjectionKnowledge(ipc, "lab", stale, map[reposupervisor.Key]repoRuntime{}, restarted)
	state := ipc.RepoState("lab", id)
	if state == nil {
		t.Fatal("preserved folder not projected")
	}
	summary := state.Summary()
	if !summary.ServerDeleted || !summary.LocalCopyPreserved || summary.Attached || summary.LocalPath != rec.LocalPath {
		t.Fatalf("summary=%+v", summary)
	}
}
