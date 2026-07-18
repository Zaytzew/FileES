package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/errmap"
	"filees/pkg/ipcserver"
	"filees/pkg/passport"
	"filees/pkg/reposupervisor"
	"filees/pkg/runtime"
	"filees/pkg/talk"
	"filees/pkg/watcher"
)

type repoRuntime struct {
	config config.Repo
	state  *ipcserver.RepoState
}

func openRepoErrorSink(path, scope string) (*errmap.Sink, *os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return errmap.NewSink(file, scope), file, nil
}

func buildCommitService(repo config.Repo, svn client.Client, rules commit.Rules, gate runtime.Gate, mutex runtime.RepoMutex, clientUUID string, sink *errmap.Sink, ipc *ipcserver.Server, state *ipcserver.RepoState, passports *passport.Manager) *commit.Service {
	service := &commit.Service{Cli: svn, Rules: rules, HostGate: gate, RepoMtx: mutex, Logger: talk.With("commit:" + repo.ID), RepoURL: repo.RepoURL, UUID: clientUUID, ErrSink: sink}
	if ipc != nil {
		service.Emit = func(eventType string, payload any) { ipc.Emit(ipc.NewRepoEvent(repo.ID, eventType, payload)) }
	}
	wireRepoStatus(service, state)
	if passports != nil {
		service.BeginPublish = passports.BeginPublish
		service.OnPathActivity = passports.Touch
		service.OnPathsPublished = passports.MarkPublished
		service.OnPathsRemoved = passports.ForgetRemoved
	}
	return service
}

type repoEventSource interface {
	Start(context.Context) <-chan watcher.Event
}
type repoCommitRunner interface {
	Run(context.Context, string, string, <-chan watcher.Event)
}

func runReadWritePipeline(ctx context.Context, repo config.Repo, state *ipcserver.RepoState, source repoEventSource, committer repoCommitRunner) error {
	if state == nil || source == nil || committer == nil {
		return errors.New("read-write pipeline is incomplete")
	}
	state.SetState(contract.StateActive)
	events := source.Start(ctx)
	committer.Run(ctx, repo.ID, repo.LocalPath, events)
	state.SetState(contract.StateStopping)
	return nil
}

func buildWatcherOptions(repo config.Repo, manifest, busyPath string) (watcher.Options, time.Duration) {
	window := repo.CommitInterval
	if window <= 0 {
		window = 30 * time.Second
	}
	publishLatency := 5 * time.Minute
	scan := repo.WatchInterval
	if scan <= 0 {
		scan = window / 2
	}
	return watcher.Options{WC: repo.LocalPath, StatePath: manifest, ScanPeriod: scan, BusyPath: busyPath, BusyTTL: 10 * time.Minute, TicketsPoll: 12 * time.Second, DeletedDebounce: publishLatency, LogScope: "watch:" + repo.ID, UseMD5: true, ChanSize: 1024}, publishLatency
}

func buildCommitRules(repo config.Repo, publishLatency time.Duration) commit.Rules {
	window := repo.CommitInterval
	if window <= 0 {
		window = 30 * time.Second
	}
	poll := repo.PollInterval
	if poll <= 0 {
		poll = 30 * time.Second
	}
	tiers := make([]commit.SizeTier, len(repo.CommitTiers))
	for i, t := range repo.CommitTiers {
		tiers[i] = commit.SizeTier{MaxBytes: int64(t.MaxMB * 1024 * 1024), Interval: t.Interval}
	}
	return commit.Rules{Window: window, MaxBatchFiles: intOrDefault(repo.MaxBatchFiles, 100), MaxBatchBytes: mibOrDefault(repo.MaxBatchMiB, 512), BacklogFlushBytes: mibOrDefault(repo.BacklogFlushMiB, 1024), ShutdownTimeout: durationOrDefault(repo.ShutdownCommitTimeout, 10*time.Minute), ShoutPatterns: config.MustCompileRegex(repo.ShoutPatterns), LockFirst: repo.LockFirst && !repo.EditPassports, NeedsLock: repo.EditPassports, RateLimitShout: repo.RateLimitShout, NewLatency: publishLatency, SizeTiers: tiers, PollInterval: poll}
}

type passportRunner interface {
	Run(context.Context)
	ReleaseAll(context.Context) error
}

type passportSession struct {
	cancel   context.CancelFunc
	done     chan struct{}
	manager  passportRunner
	once     sync.Once
	stopDone chan struct{}
	err      error
}

func startPassportSession(parent context.Context, manager passportRunner) (*passportSession, error) {
	if manager == nil {
		return nil, errors.New("passport manager is required")
	}
	ctx, cancel := context.WithCancel(parent)
	s := &passportSession{cancel: cancel, done: make(chan struct{}), manager: manager, stopDone: make(chan struct{})}
	go func() { defer close(s.done); manager.Run(ctx) }()
	return s, nil
}

func (s *passportSession) Stop(ctx context.Context) error {
	s.once.Do(func() { go s.stop() })
	select {
	case <-s.stopDone:
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *passportSession) stop() {
	defer close(s.stopDone)
	s.cancel()
	<-s.done
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.err = s.manager.ReleaseAll(ctx)
}

func recoverReadWriteWorkingCopy(ctx context.Context, svn client.Client, wc string, service *commit.Service, logger talk.Logger) {
	if _, err := os.Stat(filepath.Join(wc, ".svn")); err != nil {
		return
	}
	if out, err := svn.Cleanup(ctx, wc); err != nil {
		logger.Warnf("svn cleanup failed: %v %s", err, out)
	}
	status, err := svn.Status(ctx, wc, nil)
	if err != nil {
		logger.Warnf("svn status before update failed: %v — update deferred", err)
		return
	}
	if client.HasMissingPaths(status) {
		logger.Infof("svn update deferred: working copy contains local removals")
		return
	}
	out, err := svn.Update(ctx, wc)
	service.ReconcileUpdateConflicts(ctx, wc, out)
	if err != nil {
		logger.Warnf("svn update failed: %v %s", err, out)
	}
}

type svnFactory func(config.Repo) client.Client
type readWriteFactory func(context.Context, repoRuntime, client.Client, reposupervisor.Desired) (reposupervisor.Instance, error)

type readWriteDependencies struct {
	gate  runtime.Gate
	mutex runtime.RepoMutex
	ipc   *ipcserver.Server
}

func startReadWrite(ctx context.Context, runtimeRepo repoRuntime, svn client.Client, desired reposupervisor.Desired, deps readWriteDependencies) (reposupervisor.Instance, error) {
	repo := runtimeRepo.config
	wc := repo.LocalPath
	logger := talk.With("repo:" + repo.ID)
	stateDir := filepath.Join(wc, ".filees", "state")
	logsDir := filepath.Join(wc, ".filees", "logs")
	for _, dir := range []string{stateDir, filepath.Join(wc, ".filees", "tickets"), filepath.Join(wc, ".filees", "locks", "global"), filepath.Join(wc, ".filees", "locks", "repo"), logsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("init directory %s: %w", dir, err)
		}
	}
	manifest := filepath.Join(stateDir, "manifest.json")
	tmpManifest := filepath.Join(stateDir, "manifest.tmp")
	baselineOK := filepath.Join(stateDir, "baseline.ok")
	busyPath := filepath.Join(stateDir, "commit.busy")
	pidPath := filepath.Join(stateDir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		return nil, err
	}
	cleanupPID := true
	defer func() {
		if cleanupPID {
			_ = os.Remove(pidPath)
		}
	}()
	clientUUID := loadOrCreateUUID(filepath.Join(stateDir, "client.uuid"))
	var manager *passport.Manager
	var passports *passportSession
	if repo.EditPassports {
		var err error
		manager, err = passport.Open(filepath.Join(wc, ".filees", "passports", "passports.json"), clientUUID, passport.SVNBackend{Client: svn, WC: wc}, passport.Config{TTL: repo.EditPassportTTL, HeartbeatInterval: repo.EditPassportHeartbeat, MaxSession: repo.EditPassportMaxSession, CloseGrace: repo.EditPassportCloseGrace})
		if err != nil {
			return nil, err
		}
		passports, err = startPassportSession(ctx, manager)
		if err != nil {
			return nil, err
		}
	}
	rollbackPassport := true
	defer func() {
		if rollbackPassport && passports != nil {
			_ = passports.Stop(context.Background())
		}
	}()
	if fileExists(baselineOK) && fileExists(tmpManifest) && !fileExists(manifest) {
		if err := os.Rename(tmpManifest, manifest); err != nil {
			return nil, err
		}
		_ = os.Remove(baselineOK)
	}
	wopts, latency := buildWatcherOptions(repo, manifest, busyPath)
	scanner, err := watcher.NewScanner(wopts)
	if err != nil {
		return nil, err
	}
	rules := buildCommitRules(repo, latency)
	sink, errorFile, err := openRepoErrorSink(filepath.Join(logsDir, "errors.jsonl"), "commit:"+repo.ID)
	if err != nil {
		logger.Warnf("structured errors disabled: %v", err)
		sink = nil
		errorFile = nil
	}
	service := buildCommitService(repo, svn, rules, deps.gate, deps.mutex, clientUUID, sink, deps.ipc, runtimeRepo.state, manager)
	recoverReadWriteWorkingCopy(ctx, svn, wc, service, logger)
	if manager != nil {
		if err := passport.EnsureNeedsLock(ctx, svn, wc, clientUUID, intOrDefault(repo.MaxBatchFiles, 100)); err != nil {
			if errorFile != nil {
				_ = errorFile.Close()
			}
			return nil, err
		}
	}
	instance, err := reposupervisor.StartManaged(ctx, func(runCtx context.Context) error {
		return runReadWritePipeline(runCtx, repo, runtimeRepo.state, scanner, service)
	}, func(cleanupCtx context.Context) error {
		var first error
		if passports != nil {
			if err := passports.Stop(cleanupCtx); err != nil {
				first = err
			}
		}
		if errorFile != nil {
			if err := errorFile.Close(); err != nil && first == nil {
				first = err
			}
		}
		if err := os.Remove(pidPath); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
		return first
	})
	if err != nil {
		if errorFile != nil {
			_ = errorFile.Close()
		}
		return nil, err
	}
	cleanupPID = false
	rollbackPassport = false
	_ = desired
	return instance, nil
}

// daemonRepoStarter binds generic supervisor lifecycle to concrete daemon
// pipelines. daemonCtx, not the reconcile context, owns running instances.
type daemonRepoStarter struct {
	daemonCtx      context.Context
	repos          map[reposupervisor.Key]repoRuntime
	newSVN         svnFactory
	startReadWrite readWriteFactory
}

func (s *daemonRepoStarter) Start(startCtx context.Context, desired reposupervisor.Desired) (reposupervisor.Instance, error) {
	if err := startCtx.Err(); err != nil {
		return nil, err
	}
	runtime, ok := s.repos[desired.Key]
	if !ok {
		return nil, fmt.Errorf("repository attachment %s is not configured", desired.Key)
	}
	if s.daemonCtx == nil || s.newSVN == nil || runtime.state == nil {
		return nil, errors.New("repository starter is incomplete")
	}
	svn := s.newSVN(runtime.config)
	if desired.Access == contract.AccessReadWrite {
		if s.startReadWrite == nil {
			return nil, errors.New("read-write pipeline factory is not connected yet")
		}
		return s.startReadWrite(s.daemonCtx, runtime, svn, desired)
	}
	if desired.Access != contract.AccessReadOnly {
		return nil, errors.New("repository access is invalid")
	}
	stateDir := filepath.Join(runtime.config.LocalPath, ".filees", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	pidPath := filepath.Join(stateDir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		return nil, err
	}
	return reposupervisor.StartManaged(s.daemonCtx, func(ctx context.Context) error {
		runReadOnlyRepo(ctx, runtime.config, runtime.state, svn, talk.With("readonly:"+desired.Key.String()))
		return nil
	}, func(context.Context) error {
		if err := os.Remove(pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}
