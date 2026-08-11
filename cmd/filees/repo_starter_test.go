package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientview"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/errmap"
	"filees/pkg/ipcserver"
	"filees/pkg/passport"
	"filees/pkg/reposupervisor"
	"filees/pkg/talk"
	"filees/pkg/watcher"
)

func TestReadOnlyStarterUsesDaemonLifecycleNotReconcileContext(t *testing.T) {
	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()
	key := reposupervisor.Key{ServerID: "office", RepoID: "archive"}
	url := "svn+ssh://example/archive"
	wc := t.TempDir()
	if err := os.Mkdir(filepath.Join(wc, ".svn"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := ipcserver.New(t.TempDir() + "/sock")
	state := server.RegisterRepoAccess(key.RepoID, url, wc, key.ServerID, contract.AccessReadOnly)
	fake := &updateOnlyClient{called: make(chan struct{}, 4), infoURL: url}
	starter := &daemonRepoStarter{daemonCtx: daemonCtx, repos: map[reposupervisor.Key]repoRuntime{key: {config: config.Repo{ID: key.RepoID, LocalPath: wc, PollInterval: 10 * time.Millisecond}, state: state}}, newSVN: func(config.Repo) client.Client { return fake }}
	reconcileCtx, cancel := context.WithCancel(context.Background())
	instance, err := starter.Start(reconcileCtx, reposupervisor.Desired{Key: key, Access: "r", State: "active", URL: url})
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

func TestRepoErrorSinkDoesNotPinWorkingCopyDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".filees", "logs", "errors.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	sink, err := openRepoErrorSink(path, "commit:docs")
	if err != nil || sink == nil {
		t.Fatalf("sink=%v err=%v", sink, err)
	}
	sink.EmitAt(time.Unix(1, 0), errmap.Classify(errors.New("probe")))
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("structured log was not written: info=%v err=%v", info, err)
	}
	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatalf("structured error sink pinned working-copy directory: %v", err)
	}
	if err := os.Rename(moved, root); err != nil {
		t.Fatal(err)
	}
}

func TestCommitServiceBuilderWiresRuntimeAndIPCState(t *testing.T) {
	repo := config.Repo{ID: "docs", RepoURL: "svn+ssh://_filees-client@example/docs"}
	svn := &updateOnlyClient{called: make(chan struct{}, 1)}
	state := ipcserver.New(t.TempDir()+"/sock").RegisterRepoAccess(repo.ID, repo.RepoURL, t.TempDir(), "office", contract.AccessReadWrite)
	rules := commit.Rules{Window: time.Minute}
	service := buildCommitService(repo, svn, rules, nil, nil, "client-uuid", nil, nil, state, nil, nil)
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

type lockClient struct {
	client.Client
	locked, unlocked [][]string
}

func (f *lockClient) Lock(_ context.Context, _ string, paths []string) (string, error) {
	f.locked = append(f.locked, append([]string{}, paths...))
	return "locked", nil
}

func (f *lockClient) Unlock(_ context.Context, _ string, paths []string) (string, error) {
	f.unlocked = append(f.unlocked, append([]string{}, paths...))
	return "unlocked", nil
}

type passportBackend struct {
	lock *passport.Lock
	seq  int
}

func (b *passportBackend) Inspect(context.Context, string) (*passport.Lock, error) {
	if b.lock == nil {
		return nil, nil
	}
	copy := *b.lock
	return &copy, nil
}

func (b *passportBackend) Lock(_ context.Context, _ string, comment string, _ bool) (*passport.Lock, string, error) {
	b.seq++
	b.lock = &passport.Lock{Token: fmt.Sprintf("token-%d", b.seq), Owner: "client", Comment: comment}
	copy := *b.lock
	return &copy, "passport locked", nil
}

func (b *passportBackend) Unlock(context.Context, string) (string, error) {
	b.lock = nil
	return "passport unlocked", nil
}

func TestRepoLockFunctionsUseSVNOrEditPassportManager(t *testing.T) {
	server := ipcserver.New(filepath.Join(t.TempDir(), "daemon.sock"))
	state := server.RegisterRepoAccess("docs", "svn+ssh://example/docs", t.TempDir(), "office", contract.AccessReadWrite)
	wcRoot := filepath.Join(t.TempDir(), "wc", "docs")
	plain := &lockClient{}
	wireRepoLockFuncs(state, plain, wcRoot, nil)
	plainPath := filepath.Join(wcRoot, "a")
	if out, err := state.Lock(t.Context(), []string{plainPath}); err != nil || out != "locked" {
		t.Fatalf("plain lock out=%q err=%v", out, err)
	}
	if out, err := state.Unlock(t.Context(), []string{plainPath}); err != nil || out != "unlocked" {
		t.Fatalf("plain unlock out=%q err=%v", out, err)
	}
	if len(plain.locked) != 1 || len(plain.unlocked) != 1 {
		t.Fatalf("plain lock calls=%v unlock calls=%v", plain.locked, plain.unlocked)
	}

	backend := &passportBackend{}
	manager, err := passport.Open(filepath.Join(t.TempDir(), "passports.json"), "instance", backend, passport.Config{})
	if err != nil {
		t.Fatal(err)
	}
	wireRepoLockFuncs(state, nil, wcRoot, manager)
	path := filepath.Join(wcRoot, "edited.dwg")
	if _, err := state.Lock(t.Context(), []string{path}); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Snapshot()
	if len(snapshot) != 1 || snapshot[0].Path != path || backend.lock == nil {
		t.Fatalf("passport acquisition snapshot=%+v backend=%+v", snapshot, backend.lock)
	}
	if _, err := state.Unlock(t.Context(), []string{path}); err != nil {
		t.Fatal(err)
	}
	if len(manager.Snapshot()) != 0 || backend.lock != nil {
		t.Fatalf("passport was not released: snapshot=%+v backend=%+v", manager.Snapshot(), backend.lock)
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
	wc := t.TempDir()
	if err := os.Mkdir(filepath.Join(wc, ".svn"), 0o755); err != nil {
		t.Fatal(err)
	}
	starter := &daemonRepoStarter{daemonCtx: daemonCtx, repos: map[reposupervisor.Key]repoRuntime{key: {config: config.Repo{ID: key.RepoID, RepoURL: "svn+ssh://_filees-client@old.example/documents", LocalPath: wc, Access: contract.AccessReadOnly}, state: state}}, newSVN: func(repo config.Repo) client.Client {
		if repo.RepoURL != projectedURL || repo.Access != contract.AccessReadWrite {
			t.Fatalf("SVN factory received stale authority: %+v", repo)
		}
		return &updateOnlyClient{called: make(chan struct{}, 1), infoURL: projectedURL}
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

func TestStarterDoesNotRecreateMissingWorkingCopyAndRecoversWhenItReturns(t *testing.T) {
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	defer cancelDaemon()
	key := reposupervisor.Key{ServerID: "office", RepoID: "documents"}
	url := "svn+ssh://example/documents"
	wc := filepath.Join(t.TempDir(), "renamed-working-copy")
	state := ipcserver.New(t.TempDir()+"/sock").RegisterRepoAccess(key.RepoID, url, wc, key.ServerID, contract.AccessReadWrite)
	started := make(chan struct{}, 1)
	starter := &daemonRepoStarter{
		daemonCtx: daemonCtx,
		repos: map[reposupervisor.Key]repoRuntime{key: {
			config: config.Repo{ID: key.RepoID, RepoURL: url, LocalPath: wc, Access: contract.AccessReadWrite},
			state:  state,
		}},
		newSVN:        func(config.Repo) client.Client { return &updateOnlyClient{infoURL: url} },
		retryInterval: 10 * time.Millisecond,
		startReadWrite: func(ctx context.Context, runtime repoRuntime, _ client.Client, _ reposupervisor.Desired) (reposupervisor.Instance, error) {
			runtime.state.SetState(contract.StateActive)
			started <- struct{}{}
			return reposupervisor.StartManaged(ctx, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, nil)
		},
	}
	instance, err := starter.Start(t.Context(), reposupervisor.Desired{Key: key, Access: contract.AccessReadWrite, State: "active", URL: url})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := state.Snapshot(); snapshot.State != contract.StateInteractionRequired || snapshot.CurrentOperation == nil || *snapshot.CurrentOperation != "working_copy_missing" {
		t.Fatalf("missing working copy snapshot=%+v", snapshot)
	}
	if _, err := os.Stat(wc); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing working copy was recreated: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(wc, ".svn"), 0o755); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("pipeline did not recover after working copy returned")
	}
	if snapshot := state.Snapshot(); snapshot.State != contract.StateActive || snapshot.CurrentOperation != nil {
		t.Fatalf("restored working copy snapshot=%+v", snapshot)
	}
	if err := instance.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// Only the canonical opted-in value turns passports on. The default must not,
// or every existing repository would suddenly demand locks; and an unknown
// value must not either, since a client that cannot recognise the policy also
// cannot honour the machinery behind it.
func TestPassportsRequiredOnlyForTheCanonicalOptedInPolicy(t *testing.T) {
	if !passportsRequired(clientview.EditingLockRequired) {
		t.Fatal("lock_required did not enable passports")
	}
	for _, policy := range []string{clientview.EditingFree, "free", "readonly", "LOCK_REQUIRED"} {
		if passportsRequired(policy) {
			t.Fatalf("policy %q enabled passports", policy)
		}
	}
}

// buildCommitRules derives the two commit-side switches from the same flag, so
// a repository on the default policy must not stamp svn:needs-lock on anything
// it commits - that property is versioned and would outlive the mistake.
func TestDefaultPolicyLeavesNeedsLockOffInCommitRules(t *testing.T) {
	free := config.Repo{ID: "docs", LocalPath: "/wc/docs", EditPassports: passportsRequired(clientview.EditingFree)}
	if rules := buildCommitRules(free, 5*time.Minute); rules.NeedsLock {
		t.Fatalf("default policy would stamp svn:needs-lock: %+v", rules)
	}
	locked := config.Repo{ID: "docs", LocalPath: "/wc/docs", EditPassports: passportsRequired(clientview.EditingLockRequired)}
	if rules := buildCommitRules(locked, 5*time.Minute); !rules.NeedsLock {
		t.Fatalf("opted-in policy did not enable needs-lock: %+v", rules)
	}
}
