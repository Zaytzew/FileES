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
	"filees/pkg/config"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
)

// Receipt/lifecycle integration only: this adapter does not register CFAPI,
// rename a directory, copy ignored files, or prove the native cleanup boundary.
type daemonConversionStub struct {
	calls         []string
	wait, bad     string
	afterPrepare  func()
	afterFinalize func()
	finalizeNil   bool
	releases      int
	plan          string
}

func (s *daemonConversionStub) call(name string) error {
	s.calls = append(s.calls, name)
	if s.wait == name {
		return localrepo.ErrConversionWaiting
	}
	if s.bad == name {
		return errors.New("fixture: " + name)
	}
	return nil
}
func (s *daemonConversionStub) Acquire(context.Context, localrepo.ConversionRecord) (func(), error) {
	if err := s.call("acquire"); err != nil {
		return nil, err
	}
	return func() { s.releases++ }, nil
}
func (s *daemonConversionStub) Revalidate(context.Context, localrepo.ConversionRecord) error {
	return s.call("binding")
}
func (s *daemonConversionStub) Prepare(context.Context, localrepo.ConversionRecord) (localrepo.ConversionPreparation, error) {
	err := s.call("prepare")
	if s.afterPrepare != nil {
		s.afterPrepare()
	}
	return localrepo.ConversionPreparation{SourceFileID: "source-id", Revision: 7, PlanPath: s.plan, PlanSHA256: strings.Repeat("ab", 32)}, err
}
func (s *daemonConversionStub) Retain(context.Context, localrepo.ConversionRecord) error {
	return s.call("retain")
}
func (s *daemonConversionStub) Point(context.Context, localrepo.ConversionRecord) (localrepo.ConversionPoint, error) {
	return localrepo.ConversionPoint{FileID: "point-id", ProviderID: "FileES"}, s.call("point")
}
func (s *daemonConversionStub) Preserve(context.Context, localrepo.ConversionRecord) error {
	return s.call("preserve")
}
func (s *daemonConversionStub) Verify(context.Context, localrepo.ConversionRecord) error {
	return s.call("verify")
}
func (s *daemonConversionStub) Cleanup(context.Context, localrepo.ConversionRecord) error {
	return s.call("cleanup")
}
func (s *daemonConversionStub) Rollback(_ context.Context, r localrepo.ConversionRecord) (string, error) {
	return r.SourceFileID, s.call("rollback")
}
func (s *daemonConversionStub) Finalize(context.Context, localrepo.ConversionRecord) (func(), error) {
	if s.afterFinalize != nil {
		s.afterFinalize()
	}
	if err := s.call("finalize"); err != nil {
		return nil, err
	}
	if s.finalizeNil {
		return nil, nil
	}
	return func() { s.releases++ }, nil
}

type daemonConversionFixture struct {
	p         *daemonProvisioner
	r         localrepo.Record
	effects   *daemonConversionStub
	events    chan provisionedAttachment
	storePath string
}

func newDaemonConversionFixture(t *testing.T) daemonConversionFixture {
	t.Helper()
	base := t.TempDir()
	storePath := filepath.Join(base, "lifecycle.json")
	s, err := localrepo.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	j, err := provisioning.NewStore(filepath.Join(base, "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.EnsureConfiguredAttached("lab", "repo", "svn+ssh://fixture/repo", "rw", filepath.Join(base, "WC"), "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.LocalPath, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ensureWorkingCopyIdentity(r.LocalPath, expectedWorkingCopyIdentity(r.ServerID, r.RepoID, r.RepoURL)); err != nil {
		t.Fatal(err)
	}
	p := newDaemonProvisioner(s, j, []clientprofile.Profile{{ServerID: r.ServerID, ClientID: "activation"}})
	p.conversionRoot = filepath.Join(base, "conversions")
	p.anchorEnabled = func(string) bool { return true }
	p.relocationCheck = func(localrepo.Record) error { return nil }
	effects := &daemonConversionStub{plan: filepath.Join(base, "plan.json")}
	p.newConversionEffects = func(localrepo.Record, localrepo.ConversionRecord) (conversionEffectsAdapter, error) {
		return effects, nil
	}
	events := make(chan provisionedAttachment, 16)
	p.attachments = events
	return daemonConversionFixture{p, r, effects, events, storePath}
}

func (f daemonConversionFixture) queue(t *testing.T) localrepo.Record {
	t.Helper()
	r, err := f.p.queueConversion(t.Context(), f.r.ServerID, f.r.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f daemonConversionFixture) run(t *testing.T, ctx context.Context, quiesceErrors ...error) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f.p.runOne(ctx, f.r.OperationID) }()
	select {
	case event := <-f.events:
		if !event.Quiesce || event.Detach {
			t.Error("conversion must quiesce without detach", event)
		}
		current, _ := f.p.local.Get(f.r.OperationID)
		if current.State != localrepo.StateRelocating || current.ConversionID == "" {
			t.Error("runtime stopped without durable execution fence", current)
		}
		var quiesceErr error
		if len(quiesceErrors) > 0 {
			quiesceErr = quiesceErrors[0]
		}
		event.Result <- quiesceErr
	case <-time.After(5 * time.Second):
		t.Fatal("conversion did not request supervisor quiescence")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("conversion worker did not finish")
	}
}

func TestDaemonConversionQueueOwnsSingleJournalAndMetadataPreservingFence(t *testing.T) {
	f := newDaemonConversionFixture(t)
	f.p.localIntentMu.Lock()
	r := f.queue(t)
	duplicate := f.queue(t)
	if duplicate != r || len(f.events) != 0 {
		t.Fatal("queue stopped runtime or replaced intent")
	}
	j, err := f.p.conversionJournal(r, false)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.p.conversionJournal(r, false)
	if err != nil || j != again {
		t.Fatal("more than one journal owner")
	}
	f.p.localIntentMu.Unlock()
	f.run(t, t.Context())
	finished, _ := f.p.local.Get(r.OperationID)
	if finished.State != localrepo.StateAttached || !finished.Anchor || !finished.Sparse || finished.ConversionID != "" || finished.LastError != "" {
		t.Fatal(finished)
	}
	if f.effects.releases != 2 || strings.Join(f.effects.calls, ",") == "" {
		t.Fatal(f.effects)
	}
	event := <-f.events
	if event.Quiesce || !event.Repo.Anchor || !event.Repo.Sparse {
		t.Fatal("wrong runtime projection", event)
	}
	if _, err := os.Stat(filepath.Join(r.LocalPath, ".svn")); err != nil {
		t.Fatal("detach stripped original metadata", err)
	}
	if _, err := os.Stat(workingCopyIdentityPath(r.LocalPath)); err != nil {
		t.Fatal("detach stripped identity", err)
	}
	// The older create journal must also publish the new mode after restart.
	f.p.publishAttachment(t.Context(), provisioning.Operation{OperationID: r.OperationID, RepoID: r.RepoID, RepoURL: r.RepoURL, LocalPath: r.LocalPath, State: provisioning.StateActive})
	restored := <-f.events
	if !restored.Repo.Anchor || !restored.Repo.Sparse {
		t.Fatal("old create journal revived full writer", restored)
	}
	configured, err := reconcileConfiguredRepositoryLifecycle(f.p.local, []config.Repo{{ID: r.RepoID, ServerID: r.ServerID, RepoURL: r.RepoURL, LocalPath: r.LocalPath}})
	if err != nil || len(configured) != 1 || !configured[0].Anchor || !configured[0].Sparse {
		t.Fatal("static config revived full writer after conversion", configured, err)
	}
}

func TestDaemonConversionWaitingResumesOnlyOriginalWriter(t *testing.T) {
	f := newDaemonConversionFixture(t)
	r := f.queue(t)
	f.effects.wait = "prepare"
	f.run(t, t.Context())
	deferred, _ := f.p.local.Get(r.OperationID)
	if deferred.State != localrepo.StateAttached || deferred.ConversionID != r.ConversionID || deferred.LastError != "" || deferred.Anchor {
		t.Fatal(deferred)
	}
	event := <-f.events
	if event.Repo.Anchor || event.Repo.LocalPath != r.LocalPath {
		t.Fatal("waiting resumed wrong root", event)
	}
	j, _ := f.p.conversionJournal(r, false)
	if j.Get().Prepared || j.Get().Blocked != "" {
		t.Fatal(j.Get())
	}
	f.effects.wait = ""
	f.run(t, t.Context())
	finished, _ := f.p.local.Get(r.OperationID)
	if !finished.Anchor {
		t.Fatal(finished)
	}
}

func TestDaemonConversionQuiesceBusyWaitsButUnknownErrorBlocks(t *testing.T) {
	for _, busy := range []bool{true, false} {
		t.Run(map[bool]string{true: "busy", false: "error"}[busy], func(t *testing.T) {
			f := newDaemonConversionFixture(t)
			r := f.queue(t)
			cause := errors.New("fixture supervisor failure")
			if busy {
				cause = commit.ErrLocalStatusBusy
			}
			f.run(t, t.Context(), cause)
			got, _ := f.p.local.Get(r.OperationID)
			if len(f.effects.calls) != 0 {
				t.Fatal("native effects started before quiescence", f.effects.calls)
			}
			if busy {
				if got.State != localrepo.StateAttached || got.LastError != "" || len(f.events) != 1 {
					t.Fatal("busy became blocking failure", got)
				}
			} else if got.State != localrepo.StateRelocating || got.LastError == "" || len(f.events) != 0 {
				t.Fatal("unknown error reopened writer", got)
			}
		})
	}
}

func TestDaemonConversionBlockedDoesNotRetryOrFallbackToRename(t *testing.T) {
	f := newDaemonConversionFixture(t)
	r := f.queue(t)
	f.effects.bad = "retain"
	f.run(t, t.Context())
	blocked, _ := f.p.local.Get(r.OperationID)
	if blocked.LastError == "" || blocked.State != localrepo.StateRelocating {
		t.Fatal(blocked)
	}
	count := len(f.effects.calls)
	f.p.runOne(t.Context(), r.OperationID)
	if len(f.effects.calls) != count || len(f.events) != 0 {
		t.Fatal("blocked operation retried")
	}
	if _, err := os.Stat(r.LocalPath); err != nil {
		t.Fatal("ordinary relocation ran as fallback", err)
	}
	if _, err := os.Stat(r.PendingLocalPath); !os.IsNotExist(err) {
		t.Fatal("ordinary relocation created backup", err)
	}
}

func TestDaemonConversionRestartAfterTerminalReceiptRequiresPinnedFinalization(t *testing.T) {
	for _, missingPin := range []bool{false, true} {
		t.Run(map[bool]string{false: "pinned", true: "missing-pin"}[missingPin], func(t *testing.T) {
			f := newDaemonConversionFixture(t)
			r := f.queue(t)
			if _, err := f.p.local.StartConversion(r); err != nil {
				t.Fatal(err)
			}
			j, _ := f.p.conversionJournal(r, false)
			e := localrepo.ConversionExecutor{Journal: j, Effects: f.effects}
			if _, err := e.Run(t.Context()); err != nil {
				t.Fatal(err)
			}
			// Reopen both durable files, discard all in-memory executor state.
			s, err := localrepo.Open(f.storePath)
			if err != nil {
				t.Fatal(err)
			}
			f.p.local = s
			f.p.conversionJournals = nil
			f.effects.calls = nil
			f.effects.finalizeNil = missingPin
			f.p.runOne(t.Context(), r.OperationID)
			if strings.Join(f.effects.calls, ",") != "finalize" {
				t.Fatal("terminal effects replayed", f.effects.calls)
			}
			got, _ := s.Get(r.OperationID)
			if missingPin {
				if got.State != localrepo.StateRelocating || got.LastError == "" || len(f.events) != 0 {
					t.Fatal("unproven root published", got)
				}
			} else if !got.Anchor || got.ConversionID != "" || len(f.events) != 1 {
				t.Fatal(got)
			}
		})
	}
}

func TestDaemonConversionStartupDoesNotReviveExecutingOrdinaryWriter(t *testing.T) {
	for _, executing := range []bool{false, true} {
		f := newDaemonConversionFixture(t)
		r := f.queue(t)
		if executing {
			var err error
			r, err = f.p.local.StartConversion(r)
			if err != nil {
				t.Fatal(err)
			}
		}
		active, err := reconcileConfiguredRepositoryLifecycle(f.p.local, []config.Repo{{ID: r.RepoID, ServerID: r.ServerID, RepoURL: r.RepoURL, LocalPath: r.LocalPath, Access: "rw"}})
		if err != nil || (len(active) == 0) != executing {
			t.Fatal("static config revived conversion writer", active, err)
		}
		if !f.p.restoreOperations(t.Context()) {
			t.Fatal("restore failed")
		}
		if (len(f.events) == 0) != executing {
			t.Fatal("restore published executing conversion", executing, len(f.events))
		}
	}
}

func TestDaemonConversionAuthorityWithdrawalStopsBeforeRetain(t *testing.T) {
	f := newDaemonConversionFixture(t)
	f.queue(t)
	f.effects.afterPrepare = func() {
		if err := f.p.local.ObserveRemoteDeletion(f.r.ServerID, f.r.RepoID); err != nil {
			t.Error(err)
		}
	}
	f.run(t, t.Context())
	for _, call := range f.effects.calls {
		if call == "retain" || call == "cleanup" {
			t.Fatal("effect after authority withdrawal", call)
		}
	}
	r, _ := f.p.local.Get(f.r.OperationID)
	if !r.RemoteDeletionObserved || r.ConversionID == "" || r.PreservedAlternatePath == "" || len(f.events) != 0 {
		t.Fatal(r)
	}
}

func TestDaemonConversionUnavailableDoesNotCreateJournalOrReservePaths(t *testing.T) {
	f := newDaemonConversionFixture(t)
	f.p.newConversionEffects = nil
	if _, err := f.p.queueConversion(t.Context(), f.r.ServerID, f.r.RepoID); err == nil {
		t.Fatal("incomplete native adapter enabled conversion")
	}
	r, _ := f.p.local.Get(f.r.OperationID)
	if r.ConversionID != "" || r.RelocationQueued {
		t.Fatal(r)
	}
	if _, err := os.Stat(f.p.conversionRoot); !os.IsNotExist(err) {
		t.Fatal("unavailable feature wrote journal", err)
	}
}

func TestDaemonConversionWaitsForStartupProjectionWithoutBlockingJournal(t *testing.T) {
	f := newDaemonConversionFixture(t)
	r := f.queue(t)
	f.p.conversionReady = func(localrepo.Record) bool { return false }
	f.p.runOne(t.Context(), r.OperationID)
	got, _ := f.p.local.Get(r.OperationID)
	if got != r || len(f.events) != 0 || len(f.effects.calls) != 0 {
		t.Fatal("startup absence became a refusal/effect", got)
	}
	f.p.conversionReady = func(localrepo.Record) bool { return true }
	f.run(t, t.Context())
	finished, _ := f.p.local.Get(r.OperationID)
	if !finished.Anchor {
		t.Fatal(finished)
	}
}

func TestDaemonConversionCancellationRestoresOnlyThroughSameIntent(t *testing.T) {
	f := newDaemonConversionFixture(t)
	r := f.queue(t)
	ctx, cancel := context.WithCancel(t.Context())
	f.effects.afterPrepare = cancel
	f.run(t, ctx)
	interrupted, _ := f.p.local.Get(r.OperationID)
	if interrupted.State != localrepo.StateRelocating || interrupted.ConversionID != r.ConversionID || interrupted.LastError != "" || len(f.events) != 0 {
		t.Fatal("cancellation reopened writer", interrupted)
	}
	j, _ := f.p.conversionJournal(r, false)
	if j.Get().Phase != localrepo.ConversionWaiting || j.Get().Blocked != "" {
		t.Fatal(j.Get())
	}
	f.effects.afterPrepare = nil
	f.run(t, t.Context())
	finished, _ := f.p.local.Get(r.OperationID)
	if !finished.Anchor || finished.LastError != "" {
		t.Fatal(finished)
	}
}

func TestDaemonConversionRejectsChangedTransportWithSameClientID(t *testing.T) {
	f := newDaemonConversionFixture(t)
	f.queue(t)
	f.effects.afterPrepare = func() {
		f.p.mu.Lock()
		profile := f.p.profiles[f.r.ServerID]
		profile.Address = "different-host"
		f.p.profiles[f.r.ServerID] = profile
		f.p.mu.Unlock()
	}
	f.run(t, t.Context())
	for _, call := range f.effects.calls {
		if call == "retain" || call == "cleanup" {
			t.Fatal("effect used stale transport", call)
		}
	}
	r, _ := f.p.local.Get(f.r.OperationID)
	if r.LastError == "" || r.State != localrepo.StateRelocating || len(f.events) != 0 {
		t.Fatal(r)
	}
}

func TestDaemonConversionRevalidatesAfterFinalRootPin(t *testing.T) {
	f := newDaemonConversionFixture(t)
	f.queue(t)
	f.effects.afterFinalize = func() { f.p.mu.Lock(); delete(f.p.profiles, f.r.ServerID); f.p.mu.Unlock() }
	f.run(t, t.Context())
	r, _ := f.p.local.Get(f.r.OperationID)
	if r.State != localrepo.StateRelocating || r.ConversionID == "" || r.LastError == "" || len(f.events) != 0 {
		t.Fatal("published after activation withdrawal", r)
	}
	if f.effects.releases != 2 {
		t.Fatal("final root pin leaked", f.effects.releases)
	}
}
