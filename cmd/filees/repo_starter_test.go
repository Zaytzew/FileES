package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/reposupervisor"
	"filees/pkg/talk"
	"filees/pkg/watcher"
)

func TestReadOnlyStarterUsesDaemonLifecycleNotReconcileContext(t *testing.T) {
	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()
	key := reposupervisor.Key{ServerID: "office", RepoID: "archive"}
	server := ipcserver.New(t.TempDir() + "/sock")
	state := server.RegisterRepoAccess(key.RepoID, "svn+ssh://_filees-client@example/archive", t.TempDir(), key.ServerID, contract.AccessReadOnly)
	fake := &updateOnlyClient{called: make(chan struct{}, 4)}
	starter := &daemonRepoStarter{daemonCtx: daemonCtx, repos: map[reposupervisor.Key]repoRuntime{key: {config: config.Repo{ID: key.RepoID, LocalPath: t.TempDir(), PollInterval: 10 * time.Millisecond}, state: state}}, newSVN: func(config.Repo) client.Client { return fake }}
	reconcileCtx, cancel := context.WithCancel(context.Background())
	instance, err := starter.Start(reconcileCtx, reposupervisor.Desired{Key: key, Access: "r", State: "active"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-fake.called:
	case <-time.After(time.Second):
		t.Fatal("initial update missing")
	}
	select {
	case <-fake.called:
	case <-time.After(time.Second):
		t.Fatal("pipeline died with reconcile context")
	}
	if err := instance.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type fakeEventSource struct {
	started chan struct{}
	events  chan watcher.Event
}

func (f *fakeEventSource) Start(context.Context) <-chan watcher.Event {
	close(f.started)
	return f.events
}

type blockingCommitRunner struct {
	entered chan struct{}
	release chan struct{}
}

func (f *blockingCommitRunner) Run(context.Context, string, string, <-chan watcher.Event) {
	close(f.entered)
	<-f.release
}

func TestReadWritePipelineOwnsWatcherCommitterAndStateOrder(t *testing.T) {
	server := ipcserver.New(t.TempDir() + "/sock")
	state := server.RegisterRepoAccess("docs", "svn+ssh://_filees-client@example/docs", t.TempDir(), "office", contract.AccessReadWrite)
	source := &fakeEventSource{started: make(chan struct{}), events: make(chan watcher.Event)}
	runner := &blockingCommitRunner{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- runReadWritePipeline(t.Context(), config.Repo{ID: "docs", LocalPath: t.TempDir()}, state, source, runner)
	}()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("watcher not started")
	}
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		t.Fatal("committer not started")
	}
	if got := state.Snapshot().State; got != contract.StateActive {
		t.Fatalf("state=%s", got)
	}
	select {
	case err := <-done:
		t.Fatalf("pipeline returned early: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(runner.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := state.Snapshot().State; got != contract.StateStopping {
		t.Fatalf("state=%s", got)
	}
}

func TestReadWriteBuildersPreserveDefaultsAndPassportLockPolicy(t *testing.T) {
	repo := config.Repo{ID: "docs", LocalPath: "/wc/docs", LockFirst: true, EditPassports: true, CommitTiers: []config.TierSpec{{MaxMB: 2, Interval: time.Minute}}}
	wopts, latency := buildWatcherOptions(repo, "/wc/docs/.filees/state/manifest.json", "/wc/docs/.filees/state/commit.busy")
	if wopts.ScanPeriod != 15*time.Second || wopts.DeletedDebounce != 5*time.Minute || !wopts.UseMD5 || wopts.ChanSize != 1024 {
		t.Fatalf("watcher=%+v", wopts)
	}
	rules := buildCommitRules(repo, latency)
	if rules.Window != 30*time.Second || rules.PollInterval != 30*time.Second || rules.MaxBatchFiles != 100 || rules.MaxBatchBytes != 512*1024*1024 || rules.BacklogFlushBytes != 1024*1024*1024 {
		t.Fatalf("rules=%+v", rules)
	}
	if rules.LockFirst || !rules.NeedsLock {
		t.Fatalf("passport lock policy=%+v", rules)
	}
	if len(rules.SizeTiers) != 1 || rules.SizeTiers[0].MaxBytes != 2*1024*1024 {
		t.Fatalf("tiers=%+v", rules.SizeTiers)
	}
}

func TestRepoErrorSinkReturnsOwnedClosableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "errors.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	sink, file, err := openRepoErrorSink(path, "commit:docs")
	if err != nil || sink == nil || file == nil {
		t.Fatalf("sink=%v file=%v err=%v", sink, file, err)
	}
	if _, err := file.Write([]byte("probe\n")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("late\n")); err == nil {
		t.Fatal("write succeeded after lifecycle close")
	}
}

func TestCommitServiceBuilderWiresRuntimeAndIPCState(t *testing.T) {
	repo := config.Repo{ID: "docs", RepoURL: "svn+ssh://_filees-client@example/docs"}
	svn := &updateOnlyClient{called: make(chan struct{}, 1)}
	state := ipcserver.New(t.TempDir()+"/sock").RegisterRepoAccess(repo.ID, repo.RepoURL, t.TempDir(), "office", contract.AccessReadWrite)
	rules := commit.Rules{Window: time.Minute}
	service := buildCommitService(repo, svn, rules, nil, nil, "client-uuid", nil, nil, state, nil)
	if service.Cli != svn || service.RepoURL != repo.RepoURL || service.UUID != "client-uuid" || service.Rules.Window != time.Minute {
		t.Fatalf("service=%+v", service)
	}
	if service.BeginPublish != nil || service.OnPathActivity != nil || service.OnPathsPublished != nil || service.OnPathsRemoved != nil {
		t.Fatal("passport callbacks wired without manager")
	}
	service.OnHeadRevision(42)
	service.OnConnectivity("offline")
	snapshot := state.Snapshot()
	if snapshot.HeadRevision != 42 || snapshot.Connectivity != contract.ConnOffline || snapshot.State != contract.StateOffline {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

type fakePassportRunner struct {
	runExited         chan struct{}
	releaseCalls      atomic.Int32
	releaseBeforeExit atomic.Bool
}

func (f *fakePassportRunner) Run(ctx context.Context) { <-ctx.Done(); close(f.runExited) }
func (f *fakePassportRunner) ReleaseAll(context.Context) error {
	select {
	case <-f.runExited:
	default:
		f.releaseBeforeExit.Store(true)
	}
	f.releaseCalls.Add(1)
	return nil
}

func TestPassportSessionStopsRunBeforeReleaseAllExactlyOnce(t *testing.T) {
	fake := &fakePassportRunner{runExited: make(chan struct{})}
	session, err := startPassportSession(context.Background(), fake)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := session.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if fake.releaseBeforeExit.Load() || fake.releaseCalls.Load() != 1 {
		t.Fatalf("release before exit=%v calls=%d", fake.releaseBeforeExit.Load(), fake.releaseCalls.Load())
	}
}

type recoveryClient struct {
	client.Client
	cleanup, status, update int
	entries                 []client.StatusEntry
	statusErr               error
}

func (f *recoveryClient) Cleanup(context.Context, string) (string, error) {
	f.cleanup++
	return "", nil
}
func (f *recoveryClient) Status(context.Context, string, []string) ([]client.StatusEntry, error) {
	f.status++
	return f.entries, f.statusErr
}
func (f *recoveryClient) Update(context.Context, string) (string, error) { f.update++; return "", nil }

func TestReadWriteRecoveryDefersUpdateForMissingPathsOrStatusFailure(t *testing.T) {
	for _, fake := range []*recoveryClient{{entries: []client.StatusEntry{{Path: "gone", Item: "missing"}}}, {statusErr: errors.New("status failed")}} {
		wc := t.TempDir()
		if err := os.Mkdir(filepath.Join(wc, ".svn"), 0o755); err != nil {
			t.Fatal(err)
		}
		recoverReadWriteWorkingCopy(t.Context(), fake, wc, &commit.Service{}, talk.With("test-recovery"))
		if fake.cleanup != 1 || fake.status != 1 || fake.update != 0 {
			t.Fatalf("calls cleanup=%d status=%d update=%d", fake.cleanup, fake.status, fake.update)
		}
	}
}

func TestReadWriteRecoveryUpdatesCleanWorkingCopy(t *testing.T) {
	wc := t.TempDir()
	if err := os.Mkdir(filepath.Join(wc, ".svn"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &recoveryClient{}
	recoverReadWriteWorkingCopy(t.Context(), fake, wc, &commit.Service{}, talk.With("test-recovery"))
	if fake.cleanup != 1 || fake.status != 1 || fake.update != 1 {
		t.Fatalf("calls cleanup=%d status=%d update=%d", fake.cleanup, fake.status, fake.update)
	}
}

func TestReadWriteStarterReceivesDaemonContextAndRuntime(t *testing.T) {
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	defer cancelDaemon()
	key := reposupervisor.Key{ServerID: "office", RepoID: "documents"}
	state := ipcserver.New(t.TempDir()+"/sock").RegisterRepoAccess(key.RepoID, "svn+ssh://_filees-client@example/documents", t.TempDir(), key.ServerID, contract.AccessReadWrite)
	called := make(chan struct{}, 1)
	projectedURL := "svn+ssh://_filees-client@new.example/documents"
	starter := &daemonRepoStarter{daemonCtx: daemonCtx, repos: map[reposupervisor.Key]repoRuntime{key: {config: config.Repo{ID: key.RepoID, RepoURL: "svn+ssh://_filees-client@old.example/documents", Access: contract.AccessReadOnly}, state: state}}, newSVN: func(repo config.Repo) client.Client {
		if repo.RepoURL != projectedURL || repo.Access != contract.AccessReadWrite {
			t.Fatalf("SVN factory received stale authority: %+v", repo)
		}
		return &updateOnlyClient{called: make(chan struct{}, 1)}
	}, startReadWrite: func(ctx context.Context, runtime repoRuntime, _ client.Client, desired reposupervisor.Desired) (reposupervisor.Instance, error) {
		if ctx != daemonCtx || runtime.state != state || desired.Key != key {
			t.Fatal("read-write factory received wrong lifecycle binding")
		}
		if runtime.config.RepoURL != projectedURL || runtime.config.Access != contract.AccessReadWrite {
			t.Fatalf("runtime received stale authority: %+v", runtime.config)
		}
		called <- struct{}{}
		return reposupervisor.StartManaged(ctx, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, nil)
	}}
	reconcileCtx, cancelReconcile := context.WithCancel(context.Background())
	instance, err := starter.Start(reconcileCtx, reposupervisor.Desired{Key: key, Access: contract.AccessReadWrite, State: "active", URL: projectedURL})
	if err != nil {
		t.Fatal(err)
	}
	cancelReconcile()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("read-write factory not called")
	}
	if err := instance.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}
