package main

import (
	"context"
	"crypto/md5"
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

type contextCommitRunner struct{ entered chan struct{} }

func (f *contextCommitRunner) Run(ctx context.Context, _ string, _ string, _ <-chan watcher.Event) {
	close(f.entered)
	<-ctx.Done()
}

func (f *blockingCommitRunner) Run(context.Context, string, string, <-chan watcher.Event) {
	close(f.entered)
	<-f.release
}

func TestReadWritePipelineOwnsWatcherCommitterAndStateOrder(t *testing.T) {
	wc := t.TempDir()
	if err := os.Mkdir(filepath.Join(wc, ".svn"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := ipcserver.New(t.TempDir() + "/sock")
	state := server.RegisterRepoAccess("docs", "svn+ssh://_filees-client@example/docs", wc, "office", contract.AccessReadWrite)
	source := &fakeEventSource{started: make(chan struct{}), events: make(chan watcher.Event)}
	runner := &blockingCommitRunner{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- runReadWritePipeline(t.Context(), config.Repo{ID: "docs", LocalPath: wc}, state, source, runner)
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

func TestReadWritePipelineStopsAndRequestsLocateWhenWorkingCopyMoves(t *testing.T) {
	parent := t.TempDir()
	wc := filepath.Join(parent, "documents")
	if err := os.MkdirAll(filepath.Join(wc, ".svn"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := ipcserver.New(t.TempDir()+"/sock").RegisterRepoAccess("docs", "svn+ssh://example/docs", wc, "office", contract.AccessReadWrite)
	source := &fakeEventSource{started: make(chan struct{}), events: make(chan watcher.Event)}
	runner := &contextCommitRunner{entered: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- runReadWritePipeline(t.Context(), config.Repo{ID: "docs", LocalPath: wc}, state, source, runner)
	}()
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		t.Fatal("pipeline did not start")
	}
	moved := filepath.Join(parent, "documents-moved")
	if err := os.Rename(wc, moved); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not stop after working copy moved")
	}
	if _, err := os.Stat(wc); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned working-copy root was recreated: %v", err)
	}
	snapshot := state.Snapshot()
	if snapshot.State != contract.StateInteractionRequired || snapshot.CurrentOperation == nil || *snapshot.CurrentOperation != "working_copy_missing" {
		t.Fatalf("moved working copy snapshot=%+v", snapshot)
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
func (f *recoveryClient) Update(context.Context, string) (string, error)  { f.update++; return "", nil }
func (f *recoveryClient) Revision(context.Context, string) (int64, error) { return 0, nil }

func TestReadWriteStarterKeepsWorkingCopySizeProjectionUntilStop(t *testing.T) {
	wc := t.TempDir()
	stateDir := filepath.Join(wc, ".filees", "state")
	if err := os.MkdirAll(filepath.Join(wc, ".svn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 42)
	path := filepath.Join(wc, "one.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := md5.Sum(data)
	manifest := fmt.Sprintf(`[{"path":".","mtime":0},{"path":"one.bin","mtime":%d,"size":42,"md5":"%x"},{"path":".filees/tickets","mtime":0}]`, info.ModTime().Unix(), digest)
	if err := os.WriteFile(filepath.Join(stateDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	server := ipcserver.New(t.TempDir() + "/sock")
	state := server.RegisterRepoAccess("docs", "svn+ssh://example/docs", wc, "office", contract.AccessReadWrite)
	repo := config.Repo{ID: "docs", RepoURL: "svn+ssh://example/docs", LocalPath: wc, Access: contract.AccessReadWrite, WatchInterval: time.Hour, PollInterval: time.Hour}
	instance, err := startReadWrite(t.Context(), repoRuntime{config: repo, state: state}, &recoveryClient{}, reposupervisor.Desired{Key: reposupervisor.Key{ServerID: "office", RepoID: "docs"}}, readWriteDependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := state.Snapshot(); !snapshot.WorkingCopySizeKnown || snapshot.WorkingCopyBytes != 42 {
		t.Fatalf("live working-copy size = %d known=%v", snapshot.WorkingCopyBytes, snapshot.WorkingCopySizeKnown)
	}
	if err := instance.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if snapshot := state.Snapshot(); snapshot.WorkingCopySizeKnown {
		t.Fatalf("stopped repository retained working-copy size callback: %+v", snapshot)
	}
}

func TestReadWriteRecoveryDefersUpdateForMissingPathsOrStatusFailure(t *testing.T) {
	for _, fake := range []*recoveryClient{{entries: []client.StatusEntry{{Path: "gone", Item: "missing"}}}, {statusErr: errors.New("status failed")}} {
		wc := t.TempDir()
		if err := os.Mkdir(filepath.Join(wc, ".svn"), 0o755); err != nil {
			t.Fatal(err)
		}
		recoverReadWriteWorkingCopy(t.Context(), fake, wc, &commit.Service{}, nil, talk.With("test-recovery"))
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
	recoverReadWriteWorkingCopy(t.Context(), fake, wc, &commit.Service{}, nil, talk.With("test-recovery"))
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

type policyMigrationClient struct {
	client.Client
	props      map[string]bool
	appendOnly map[string]bool
	status     []client.StatusEntry
	listed     int
	dels       [][]string
	sets       [][]string
	commits    int
}

func (c *policyMigrationClient) Status(context.Context, string, []string) ([]client.StatusEntry, error) {
	return c.status, nil
}
func (c *policyMigrationClient) PropList(_ context.Context, _ string, propName string) (map[string]bool, error) {
	c.listed++
	// The migration asks for two different properties; answering both with the
	// same map made every path look append-only and silently disabled the
	// rollback under test.
	if propName == passport.AppendOnlyProperty {
		return c.appendOnly, nil
	}
	return c.props, nil
}
func (c *policyMigrationClient) PropSet(_ context.Context, _ string, _, _ string, paths []string) (string, error) {
	c.sets = append(c.sets, append([]string(nil), paths...))
	return "", nil
}
func (c *policyMigrationClient) PropDel(_ context.Context, _ string, _ string, paths []string) (string, error) {
	c.dels = append(c.dels, append([]string(nil), paths...))
	return "", nil
}
func (c *policyMigrationClient) Commit(context.Context, string, []string, string) (string, error) {
	c.commits++
	return "", nil
}

func policyStateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A repository that never ran this policy must not be touched at all. The
// mobile append channel sets svn:needs-lock on uploaded files on its own, so
// a speculative rollback here would quietly destroy properties that another
// feature deliberately set.
func TestEditingPolicyMigrationLeavesRepositoriesThatNeverOptedInAlone(t *testing.T) {
	stateDir := policyStateDir(t)
	svn := &policyMigrationClient{props: map[string]bool{"photo.jpg": true}, status: []client.StatusEntry{{Path: "photo.jpg", Item: "normal"}}}

	applyEditingPolicyMigration(t.Context(), config.Repo{ID: "docs"}, svn, filepath.Dir(stateDir), stateDir, "instance", false, nil, talk.With("test"))

	if svn.listed != 0 || len(svn.dels) != 0 || svn.commits != 0 {
		t.Fatalf("untouched repository was migrated: listed=%d dels=%#v commits=%d", svn.listed, svn.dels, svn.commits)
	}
}

// Turning the policy off is a real transition and must actually roll back,
// then forget the marker so it does not run again on every later start.
func TestEditingPolicyMigrationRollsBackExactlyOnceAfterOptOut(t *testing.T) {
	stateDir := policyStateDir(t)
	if err := os.WriteFile(appliedEditingPolicyPath(stateDir), []byte(clientview.EditingLockRequired+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svn := &policyMigrationClient{props: map[string]bool{"a.bin": true}, status: []client.StatusEntry{{Path: "a.bin", Item: "normal"}}}
	wc := filepath.Dir(stateDir)

	applyEditingPolicyMigration(t.Context(), config.Repo{ID: "docs"}, svn, wc, stateDir, "instance", false, nil, talk.With("test"))
	if len(svn.dels) != 1 || svn.commits != 1 {
		t.Fatalf("rollback did not run: dels=%#v commits=%d", svn.dels, svn.commits)
	}
	if got := readAppliedEditingPolicy(stateDir); got != clientview.EditingFree {
		t.Fatalf("marker=%q, want the default after rollback", got)
	}

	svn.props = map[string]bool{}
	applyEditingPolicyMigration(t.Context(), config.Repo{ID: "docs"}, svn, wc, stateDir, "instance", false, nil, talk.With("test"))
	if len(svn.dels) != 1 || svn.commits != 1 {
		t.Fatalf("rollback repeated on a later start: dels=%#v commits=%d", svn.dels, svn.commits)
	}
}

// A dirty working copy must never take the repository down. The user needs the
// pipeline running to publish the very changes that are blocking the
// migration, so the migration defers and the marker stays put for a retry.
func TestEditingPolicyMigrationDefersOnDirtyWorkingCopyWithoutFailingStart(t *testing.T) {
	stateDir := policyStateDir(t)
	wc := filepath.Dir(stateDir)
	// The file has to exist for the migration to reach its dirty check at all;
	// entries it cannot stat are skipped as already-gone.
	if err := os.WriteFile(filepath.Join(wc, "wip.bin"), []byte("unsaved"), 0o644); err != nil {
		t.Fatal(err)
	}
	svn := &policyMigrationClient{props: map[string]bool{}, status: []client.StatusEntry{{Path: "wip.bin", Item: "modified"}}}

	applyEditingPolicyMigration(t.Context(), config.Repo{ID: "docs"}, svn, wc, stateDir, "instance", true, nil, talk.With("test"))

	if svn.commits != 0 {
		t.Fatalf("dirty working copy was committed: commits=%d", svn.commits)
	}
	if got := readAppliedEditingPolicy(stateDir); got != "" {
		t.Fatalf("marker=%q, want it unwritten so the migration retries", got)
	}
}

// Opting in runs forward and records it, which is what later allows the
// rollback to distinguish a real transition from a repository that was always
// free.
func TestEditingPolicyMigrationRecordsOptInSoRollbackCanTell(t *testing.T) {
	stateDir := policyStateDir(t)
	svn := &policyMigrationClient{props: map[string]bool{}, status: []client.StatusEntry{{Path: "a.bin", Item: "normal"}}}
	wc := filepath.Dir(stateDir)
	if err := os.WriteFile(filepath.Join(wc, "a.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	applyEditingPolicyMigration(t.Context(), config.Repo{ID: "docs"}, svn, wc, stateDir, "instance", true, nil, talk.With("test"))

	if len(svn.sets) != 1 || svn.commits != 1 {
		t.Fatalf("forward migration did not run: sets=%#v commits=%d", svn.sets, svn.commits)
	}
	if got := readAppliedEditingPolicy(stateDir); got != clientview.EditingLockRequired {
		t.Fatalf("marker=%q, want the opted-in policy recorded", got)
	}
}

type reservationClient struct {
	client.Client
	entries []client.LockEntry
}

func (c *reservationClient) ListLocks(context.Context, string) ([]client.LockEntry, error) {
	return c.entries, nil
}

// A lock taken while the repository was free carries no passport metadata, and
// because every client authenticates as the same SVN account its owner field
// cannot identify who holds it either. Once the policy is on, refusing release
// for such a lock would strand the path permanently - no passport can ever
// match it. Locks that *are* live passport holds must still be protected.
func TestLegacyRawLocksStayReleasableAfterThePolicyIsEnabled(t *testing.T) {
	wc := t.TempDir()
	server := ipcserver.New(t.TempDir() + "/sock")
	state := server.RegisterRepoAccess("docs", "svn+ssh://_filees-data@example/docs", wc, "office", contract.AccessReadWrite)
	manager, err := passport.Open(filepath.Join(t.TempDir(), "passports.json"), "instance", &passportBackend{}, passport.Config{})
	if err != nil {
		t.Fatal(err)
	}
	svn := &reservationClient{entries: []client.LockEntry{
		{Path: "legacy.bin", LockInfo: client.LockInfo{Token: "tok-legacy", Owner: "_filees-data", Comment: "locked by hand"}},
		{Path: "held.bin", LockInfo: client.LockInfo{Token: "tok-held", Owner: "_filees-data", Comment: passport.CommentPrefix + `{"passport_id":"p1","instance_uid":"other","expires_at":"2030-01-01T00:00:00Z","hard_expires_at":"2030-01-02T00:00:00Z"}`}},
	}}

	wireRepoReservationFuncs(state, svn, wc, manager)
	snap, err := state.ListReservations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Reservations) != 2 {
		t.Fatalf("rows=%+v", snap.Reservations)
	}
	byPath := map[string]contract.Reservation{}
	for _, row := range snap.Reservations {
		byPath[row.Path] = row
	}
	if !byPath["legacy.bin"].CanRelease {
		t.Fatal("legacy raw lock is unreleasable, so the path is stranded forever")
	}
	if byPath["held.bin"].CanRelease {
		t.Fatal("another client's live passport hold was offered for release")
	}
}
