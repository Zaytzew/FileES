package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/commit"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
)

func TestRelocationAcceptsIntentWhileAnotherFolderOwnsLocalGuard(t *testing.T) {
	local, journal, profile, record := relocationFixture(t)
	if _, err := local.FailRelocation(record.OperationID, errors.New("fixture")); err != nil {
		t.Fatal(err)
	}
	p := newDaemonProvisioner(local, journal, []clientprofile.Profile{profile})
	p.localIntentMu.Lock()
	defer p.localIntentMu.Unlock()
	service := repositoryLifecycleService{store: local, localIntentMu: &p.localIntentMu, onRelocate: p.Enqueue}
	result, err := service.BeginRelocate(record.ServerID, record.RepoID, record.PendingLocalPath, true)
	if err != nil {
		t.Fatalf("intent was refused instead of queued: %v", err)
	}
	if result.State != "relocation_queued" || result.PendingLocalPath != record.PendingLocalPath {
		t.Fatalf("result=%+v", result)
	}
	got, _ := local.Get(record.OperationID)
	if got.State != localrepo.StateAttached {
		t.Fatal("queue stopped current writer", got.State)
	}
}

func TestMoveLongRootAndLongDescendantsPreservesLocalBytes(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, strings.Repeat("source", 16), strings.Repeat("deep", 20), strings.Repeat("long", 15))
	target := filepath.Join(base, strings.Repeat("target", 16), strings.Repeat("deep", 20), strings.Repeat("long", 15))
	child := filepath.Join(strings.Repeat("folder", 12), "projekt-Łódź.txt")
	if len(filepath.Join(source, child)) < 300 {
		t.Fatal("fixture path too short")
	}
	if err := os.MkdirAll(filepath.Join(source, filepath.Dir(child)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, child), []byte("local changes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := moveWorkingCopyWithRetry(t.Context(), source, target, os.Rename); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(target, child)); err != nil || string(raw) != "local changes" {
		t.Fatalf("bytes=%q err=%v", raw, err)
	}
}

type queuedMoveFixture struct {
	p         *daemonProvisioner
	service   repositoryLifecycleService
	record    localrepo.Record
	storePath string
	events    chan provisionedAttachment
}

func newQueuedMoveFixture(t *testing.T) queuedMoveFixture {
	t.Helper()
	base := t.TempDir()
	storePath := filepath.Join(base, "lifecycle.json")
	local, err := localrepo.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := provisioning.NewStore(filepath.Join(base, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := local.EnsureConfiguredAttached("office", "repo", "svn+ssh://example/repo", "rw", filepath.Join(base, "old"), "Project")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(record.LocalPath, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ensureWorkingCopyIdentity(record.LocalPath, expectedWorkingCopyIdentity(record.ServerID, record.RepoID, record.RepoURL)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(record.LocalPath, "draft.txt"), []byte("unpublished"), 0600); err != nil {
		t.Fatal(err)
	}
	p := newDaemonProvisioner(local, journal, []clientprofile.Profile{{ServerID: record.ServerID, ClientID: "activation"}})
	p.newAttachmentSVN = func(clientprofile.Profile, string) attachmentSVN { return &attachmentSVNStub{url: record.RepoURL} }
	events := make(chan provisionedAttachment, 8)
	p.attachments = events
	service := repositoryLifecycleService{store: local, localIntentMu: &p.localIntentMu, onRelocate: p.Enqueue}
	result, err := service.BeginRelocate(record.ServerID, record.RepoID, filepath.Join(base, "new"), true)
	if err != nil {
		t.Fatal(err)
	}
	record, _ = local.Get(result.OperationID)
	return queuedMoveFixture{p, service, record, storePath, events}
}

func (f queuedMoveFixture) execute(t *testing.T, afterQuiesce func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case event := <-f.events:
			if !event.Quiesce {
				t.Error("execution did not quiesce")
			}
			if afterQuiesce != nil {
				afterQuiesce()
			}
			event.Result <- nil
		case <-time.After(5 * time.Second):
			t.Error("no quiesce request")
		}
	}()
	f.p.runOne(t.Context(), f.record.OperationID)
	<-done
}

func (f queuedMoveFixture) assertMoved(t *testing.T) {
	t.Helper()
	got, _ := f.p.local.Get(f.record.OperationID)
	if got.RelocationQueued || got.State != localrepo.StateAttached || got.LocalPath != f.record.PendingLocalPath || got.LastError != "" {
		t.Fatalf("not moved: %+v", got)
	}
	if raw, err := os.ReadFile(filepath.Join(got.LocalPath, "draft.txt")); err != nil || string(raw) != "unpublished" {
		t.Fatalf("draft=%q err=%v", raw, err)
	}
}

func TestQueuedRelocationSurvivesWaitCancellationAndRestart(t *testing.T) {
	f := newQueuedMoveFixture(t)
	f.p.localIntentMu.Lock()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); f.p.runOne(ctx, f.record.OperationID) }()
	until := time.Now().Add(5 * time.Second)
	for f.p.creationWaiters.Load() == 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if f.p.creationWaiters.Load() == 0 {
		t.Fatal("worker did not wait")
	}
	cancel()
	<-done
	f.p.localIntentMu.Unlock()
	if len(f.events) != 0 {
		t.Fatal("waiting move stopped runtime")
	}
	local, err := localrepo.Open(f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := local.Get(f.record.OperationID)
	if !got.RelocationQueued || got.State != localrepo.StateAttached {
		t.Fatal("lost durable queue", got)
	}
	f.p.local = local
	f.p.requeuePendingRelocations()
	f.execute(t, nil)
	f.assertMoved(t)
}

func TestQueuedRelocationWaitsForDurableFences(t *testing.T) {
	for _, name := range []string{commit.FileReleasePendingName, commit.RemoteTreePendingName, commit.RemoteFileDeletePendingName} {
		t.Run(name, func(t *testing.T) {
			f := newQueuedMoveFixture(t)
			state := filepath.Join(f.record.LocalPath, ".filees", "state")
			if err := os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			fence := filepath.Join(state, name)
			if err := os.WriteFile(fence, []byte("even corrupt proof must fence"), 0600); err != nil {
				t.Fatal(err)
			}
			f.p.runOne(t.Context(), f.record.OperationID)
			got, _ := f.p.local.Get(f.record.OperationID)
			if len(f.events) != 0 || !got.RelocationQueued || got.State != localrepo.StateAttached {
				t.Fatal("pending recovery disturbed", got)
			}
			if err := os.Remove(fence); err != nil {
				t.Fatal(err)
			} // disposable test proof only
			f.execute(t, nil)
			f.assertMoved(t)
		})
	}
}

func TestQueuedRelocationDefersFenceCreatedDuringQuiesce(t *testing.T) {
	f := newQueuedMoveFixture(t)
	fence := filepath.Join(f.record.LocalPath, ".filees", "state", commit.RemoteTreePendingName)
	f.execute(t, func() {
		if err := os.MkdirAll(filepath.Dir(fence), 0700); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(fence, []byte("pending"), 0600); err != nil {
			t.Error(err)
		}
	})
	got, _ := f.p.local.Get(f.record.OperationID)
	if !got.RelocationQueued || got.State != localrepo.StateAttached || got.LocalPath != f.record.LocalPath {
		t.Fatal("lost queue/recovery attachment", got)
	}
	event := <-f.events
	if event.Quiesce || event.Repo.LocalPath != f.record.LocalPath {
		t.Fatal("did not restore old runtime", event)
	}
	if err := os.Remove(fence); err != nil {
		t.Fatal(err)
	}
	f.execute(t, nil)
	f.assertMoved(t)
}

func TestQueuedRelocationRevalidatesAuthorityAndDestination(t *testing.T) {
	for _, changed := range []string{"authority", "destination"} {
		t.Run(changed, func(t *testing.T) {
			f := newQueuedMoveFixture(t)
			f.p.relocationCheck = func(record localrepo.Record) error {
				if f.p.localIntentMu.TryLock() {
					f.p.localIntentMu.Unlock()
					t.Error("authority check outside fence")
				}
				if changed == "authority" {
					return errors.New("permission withdrawn")
				}
				return nil
			}
			if changed == "destination" {
				if err := os.Mkdir(f.record.PendingLocalPath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			f.p.runOne(t.Context(), f.record.OperationID)
			got, _ := f.p.local.Get(f.record.OperationID)
			if got.RelocationQueued || got.LastError == "" || got.LocalPath != f.record.LocalPath || len(f.events) > 1 {
				t.Fatal("unsafe queued execution", got)
			}
			if _, err := os.Stat(filepath.Join(f.record.LocalPath, "draft.txt")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQueuedRelocationRecoversWakeupOverflowAndDeduplicates(t *testing.T) {
	f := newQueuedMoveFixture(t)
	f.p.queue = make(chan string, 1)
	f.p.queued = map[string]bool{}
	f.p.queue <- "other"
	result, err := f.service.BeginRelocate(f.record.ServerID, f.record.RepoID, f.record.PendingLocalPath, true)
	if err != nil || result.OperationID != f.record.OperationID {
		t.Fatal(result, err)
	}
	<-f.p.queue
	f.p.requeuePendingRelocations()
	if len(f.p.queue) != 1 || <-f.p.queue != f.record.OperationID {
		t.Fatal("lost wakeup")
	}
	if len(f.p.local.List()) != 1 {
		t.Fatal("duplicate move")
	}
}

func TestQueuedRelocationResumesAfterRenameWithoutRepeatingIt(t *testing.T) {
	f := newQueuedMoveFixture(t)
	if _, err := f.p.local.StartQueuedRelocation(f.record); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.record.LocalPath, f.record.PendingLocalPath); err != nil {
		t.Fatal(err)
	}
	local, err := localrepo.Open(f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	f.p.local = local
	f.execute(t, nil)
	f.assertMoved(t)
}

func TestQueuedRelocationDuplicateWakeupCannotAttachAgain(t *testing.T) {
	f := newQueuedMoveFixture(t)
	f.execute(t, nil)
	f.assertMoved(t)
	<-f.events // final attachment
	f.p.runOne(t.Context(), f.record.OperationID)
	if len(f.events) != 0 {
		t.Fatal("duplicate wakeup repeated attachment")
	}
}

func TestQueuedRelocationRunsAutomaticallyAfterFenceClears(t *testing.T) {
	f := newQueuedMoveFixture(t)
	fence := filepath.Join(f.record.LocalPath, ".filees", "state", commit.FileReleasePendingName)
	if err := os.MkdirAll(filepath.Dir(fence), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fence, []byte("pending"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); f.p.Run(ctx) }()
	go func() {
		for {
			select {
			case e := <-f.events:
				if e.Quiesce {
					e.Result <- nil
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)
	got, _ := f.p.local.Get(f.record.OperationID)
	if !got.RelocationQueued || got.State != localrepo.StateAttached {
		t.Fatal("writer fence was bypassed", got)
	}
	if err := os.Remove(fence); err != nil {
		t.Fatal(err)
	} // disposable proof
	until := time.Now().Add(8 * time.Second)
	for time.Now().Before(until) {
		got, _ = f.p.local.Get(f.record.OperationID)
		if !got.RelocationQueued && got.LocalPath == f.record.PendingLocalPath {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
	}
	f.assertMoved(t)
}

func TestCancelledRelocationNeverStartsRename(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := moveWorkingCopyWithRetry(ctx, "source", "target", func(string, string) error { t.Fatal("rename after cancellation"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
