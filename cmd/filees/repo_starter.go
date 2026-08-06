package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"filees/pkg/activity"
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

func buildCommitService(repo config.Repo, svn client.Client, rules commit.Rules, gate runtime.Gate, mutex runtime.RepoMutex, clientUUID string, sink *errmap.Sink, ipc *ipcserver.Server, state *ipcserver.RepoState, passports *passport.Manager, activityJournal *activity.Journal) *commit.Service {
	service := &commit.Service{Cli: svn, Rules: rules, HostGate: gate, RepoMtx: mutex, Logger: talk.With("commit:" + repo.ID), RepoURL: repo.RepoURL, RealmID: repo.RealmID, OwnerRealmID: repo.OwnerRealmID, UUID: clientUUID, ErrSink: sink, Activity: activityJournal}
	if ipc != nil {
		service.Emit = func(eventType string, payload any) { ipc.Emit(ipc.NewRepoEvent(repo.ID, eventType, payload)) }
	}
	wireRepoStatus(service, state)
	if passports != nil {
		beginPublish := passports.BeginPublish
		isOwner := repo.RealmID != "" && repo.RealmID == repo.OwnerRealmID
		if isOwner {
			// Real Acquire is deferred to the actual publish attempt
			// (AUTOLOCK_CREATOR_OWNERSHIP_CONCEPT_V2.md §3): the two calls
			// are sequential, not nested, so this cannot deadlock against
			// BeginPublish's own opMu hold. A foreign hold surfaces here as
			// an error and blocks publish without stealing; the local edit
			// stays staged for a later retry.
			beginPublish = func(ctx context.Context, paths []string) (func(), error) {
				if _, _, err := passports.Acquire(ctx, paths, repo.RealmID); err != nil {
					return func() {}, err
				}
				return passports.BeginPublish(ctx, paths)
			}
			service.AutoUnlockOwned = passports.AutoUnlockOwned
		}
		service.BeginPublish = beginPublish
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
	gate     runtime.Gate
	mutex    runtime.RepoMutex
	ipc      *ipcserver.Server
	activity *activity.Journal
}

func startReadWrite(ctx context.Context, runtimeRepo repoRuntime, svn client.Client, desired reposupervisor.Desired, deps readWriteDependencies) (reposupervisor.Instance, error) {
	repo := runtimeRepo.config
	wc := repo.LocalPath
	if runtimeRepo.state == nil {
		return nil, errors.New("read-write repository state is required")
	}
	logger := talk.With("repo:" + repo.ID)
	stateDir := filepath.Join(wc, ".filees", "state")
	logsDir := filepath.Join(wc, ".filees", "logs")
	for _, dir := range []string{stateDir, filepath.Join(wc, ".filees", "tickets"), filepath.Join(wc, ".filees", "locks", "global"), filepath.Join(wc, ".filees", "locks", "repo"), logsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("init directory %s: %w", dir, err)
		}
	}
	if err := hideFileesDir(filepath.Join(wc, ".filees")); err != nil {
		logger.Warnf("hide .filees directory: %v", err)
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
	service := buildCommitService(repo, svn, rules, deps.gate, deps.mutex, clientUUID, sink, deps.ipc, runtimeRepo.state, manager, deps.activity)
	recoverReadWriteWorkingCopy(ctx, svn, wc, service, logger)
	if manager != nil {
		if err := passport.EnsureNeedsLock(ctx, svn, wc, clientUUID, intOrDefault(repo.MaxBatchFiles, 100)); err != nil {
			if errorFile != nil {
				_ = errorFile.Close()
			}
			return nil, err
		}
	}
	wireRepoLockFuncs(runtimeRepo.state, svn, wc, manager)
	lockFuncsWired := true
	defer func() {
		if lockFuncsWired {
			runtimeRepo.state.SetLockFuncs(nil, nil)
			runtimeRepo.state.SetReservationFuncs(nil, nil)
		}
	}()
	instance, err := reposupervisor.StartManaged(ctx, func(runCtx context.Context) error {
		return runReadWritePipeline(runCtx, repo, runtimeRepo.state, scanner, service)
	}, func(cleanupCtx context.Context) error {
		var first error
		runtimeRepo.state.SetLockFuncs(nil, nil)
		runtimeRepo.state.SetReservationFuncs(nil, nil)
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
	lockFuncsWired = false
	_ = desired
	return instance, nil
}

func wireRepoLockFuncs(state *ipcserver.RepoState, svn client.Client, wc string, manager *passport.Manager) {
	wireRepoReservationFuncs(state, svn, wc, manager)
	if manager != nil {
		state.SetLockFuncs(
			func(ctx context.Context, paths []string) (string, error) {
				// Manual tray "Wypożycz do edycji" - deliberately realm-unaware
				// (empty realmID): this is the non-owner borrow path
				// (AUTOLOCK_CREATOR_OWNERSHIP_CONCEPT_V2.md §3.2), never a
				// silent same-realm takeover.
				_, out, err := manager.Acquire(ctx, paths, "")
				return out, err
			},
			manager.Release,
		)
		return
	}
	state.SetLockFuncs(
		func(ctx context.Context, paths []string) (string, error) {
			return svn.Lock(ctx, wc, paths)
		},
		func(ctx context.Context, paths []string) (string, error) {
			return svn.Unlock(ctx, wc, paths)
		},
	)
}

// wireRepoReservationFuncs supplies a live, server-contacting lock inventory
// for this working copy.  A reservation can only be released using the token
// returned by the immediately preceding list, and a passport-backed lock can
// only be released by the local passport manager that owns it.
func wireRepoReservationFuncs(state *ipcserver.RepoState, svn client.Client, wc string, manager *passport.Manager) {
	lister, ok := svn.(client.LockLister)
	if !ok || lister == nil {
		state.SetReservationFuncs(nil, nil)
		return
	}
	list := func(ctx context.Context) ([]contract.Reservation, error) {
		entries, err := lister.ListLocks(ctx, wc)
		if err != nil {
			return nil, err
		}
		activePassports := make(map[string]passport.Passport)
		if manager != nil {
			for _, item := range manager.Snapshot() {
				if item.State == passport.StateActive {
					activePassports[filepath.Clean(item.Path)] = item
				}
			}
		}
		rows := make([]contract.Reservation, 0, len(entries))
		for _, entry := range entries {
			absolute := filepath.Join(wc, entry.Path)
			passportItem, hasPassport := activePassports[filepath.Clean(absolute)]
			activePassport := hasPassport && passportItem.FencingToken == entry.Token
			rows = append(rows, contract.Reservation{
				RepoID:         state.Summary().ID,
				WorkingCopy:    wc,
				Path:           filepath.ToSlash(entry.Path),
				Token:          entry.Token,
				OwnerID:        entry.Owner,
				CreatedAt:      entry.Created.UTC().Format(time.RFC3339Nano),
				CanRelease:     manager == nil || activePassport,
				LocalChanges:   reservationHasLocalChanges(entry),
				ActivePassport: activePassport,
			})
		}
		return rows, nil
	}
	release := func(ctx context.Context, relativePath, expectedToken string, confirmRisk bool) error {
		rows, err := list(ctx)
		if err != nil {
			return err
		}
		var row *contract.Reservation
		for i := range rows {
			if rows[i].Path == filepath.ToSlash(relativePath) && rows[i].Token == expectedToken {
				row = &rows[i]
				break
			}
		}
		if row == nil {
			return errors.New("reservation changed or no longer exists; refresh the list")
		}
		if !row.CanRelease {
			return errors.New("reservation is not owned by this FileES instance")
		}
		if (row.LocalChanges || row.ActivePassport) && !confirmRisk {
			return errors.New("reservation has local editing risk; explicit confirmation is required")
		}
		absolute := filepath.Join(wc, filepath.FromSlash(relativePath))
		if rel, err := filepath.Rel(wc, absolute); err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			return errors.New("reservation path is outside its working copy")
		}
		if manager != nil {
			_, err := manager.Release(ctx, []string{absolute})
			return err
		}
		_, err = svn.Unlock(ctx, wc, []string{absolute})
		return err
	}
	state.SetReservationFuncs(list, release)
}

func reservationHasLocalChanges(entry client.LockEntry) bool {
	return (entry.LocalItem != "" && entry.LocalItem != "normal" && entry.LocalItem != "none") ||
		(entry.LocalProps != "" && entry.LocalProps != "normal" && entry.LocalProps != "none")
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
	// The local record is an attachment (path and runtime tuning). Effective
	// authority and endpoint always come from the validated server projection.
	runtime.config.Access = desired.Access
	runtime.config.RepoURL = desired.URL
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
	// Read-only attachments contribute to the server-menu inventory as well,
	// but intentionally receive no release callback.
	wireRepoReservationFuncs(runtime.state, svn, runtime.config.LocalPath, nil)
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
		runtime.state.SetReservationFuncs(nil, nil)
		if err := os.Remove(pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}
