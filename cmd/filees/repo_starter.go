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
	"filees/pkg/clientview"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/errmap"
	"filees/pkg/ipcserver"
	"filees/pkg/passport"
	"filees/pkg/reposupervisor"
	"filees/pkg/runtime"
	"filees/pkg/shout"
	"filees/pkg/talk"
	"filees/pkg/watcher"
)

type repoRuntime struct {
	config config.Repo
	state  *ipcserver.RepoState
}

// repoErrorWriter opens the structured error log only for the duration of a
// write. In particular, it must not retain a file handle below the working
// copy: Go's regular Windows OpenFile share mode omits FILE_SHARE_DELETE, so a
// long-lived log handle would prevent users from moving or renaming the whole
// working-copy directory.
type repoErrorWriter struct {
	path string
}

func (w repoErrorWriter) Write(raw []byte) (int, error) {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	n, writeErr := file.Write(raw)
	closeErr := file.Close()
	return n, errors.Join(writeErr, closeErr)
}

func openRepoErrorSink(path, scope string) (*errmap.Sink, error) {
	// Validate and create the log eagerly, but release the handle immediately.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return errmap.NewSink(repoErrorWriter{path: path}, scope), nil
}

func buildCommitService(repo config.Repo, svn client.Client, rules commit.Rules, gate runtime.Gate, mutex runtime.RepoMutex, clientUUID string, sink *errmap.Sink, ipc *ipcserver.Server, state *ipcserver.RepoState, passports *passport.Manager, activityJournal *activity.Journal) *commit.Service {
	service := &commit.Service{Cli: svn, Rules: rules, HostGate: gate, RepoMtx: mutex, Logger: talk.With("commit:" + repo.ID), RepoURL: repo.RepoURL, RealmID: repo.RealmID, OwnerRealmID: repo.OwnerRealmID, UUID: clientUUID, ErrSink: sink, Activity: activityJournal, RequireSVNMetadata: true}
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
	if !workingCopyMetadataAvailable(repo.LocalPath) {
		markWorkingCopyMissing(state)
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	lost := make(chan struct{}, 1)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if workingCopyMetadataAvailable(repo.LocalPath) {
					continue
				}
				lost <- struct{}{}
				cancel()
				return
			}
		}
	}()
	state.SetState(contract.StateActive)
	events := source.Start(runCtx)
	committer.Run(runCtx, repo.ID, repo.LocalPath, events)
	cancel()
	missing := !workingCopyMetadataAvailable(repo.LocalPath)
	select {
	case <-lost:
		missing = true
	default:
	}
	if missing {
		markWorkingCopyMissing(state)
		return nil
	}
	state.SetState(contract.StateStopping)
	return nil
}

func markWorkingCopyMissing(state *ipcserver.RepoState) {
	state.SetLockFuncs(nil, nil)
	state.SetReservationReleaseFunc(nil)
	state.SetPublishFunc(nil)
	state.SetNoticeFuncs(nil, nil)
	state.SetCurrentOp(stringPtr("working_copy_missing"))
	state.SetState(contract.StateInteractionRequired)
}

func workingCopyMetadataAvailable(wc string) bool {
	info, err := os.Stat(filepath.Join(wc, ".svn"))
	return err == nil && info.IsDir()
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
	return watcher.Options{WC: repo.LocalPath, StatePath: manifest, ScanPeriod: scan, BusyPath: busyPath, BusyTTL: 10 * time.Minute, TicketsPoll: 12 * time.Second, DeletedDebounce: publishLatency, LogScope: "watch:" + repo.ID, UseMD5: true, ChanSize: 1024, RequireSVNMetadata: true}, publishLatency
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

func recoverReadWriteWorkingCopy(ctx context.Context, svn client.Client, wc string, service *commit.Service, sink *errmap.Sink, logger talk.Logger) {
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
	if commit.BlocksUpdate(wc, status) {
		logger.Infof("svn update deferred: working copy contains local removals")
		return
	}
	out, err := svn.Update(ctx, wc)
	service.ReconcileUpdateConflicts(ctx, wc, out)
	if err != nil {
		recordSyncFailure(sink, logger, "svn update failed (startup recovery)", err)
		if strings.TrimSpace(out) != "" {
			logger.Warnf("svn update output: %s", out)
		}
	}
}

type svnFactory func(config.Repo) client.Client
type readWriteFactory func(context.Context, repoRuntime, client.Client, reposupervisor.Desired) (reposupervisor.Instance, error)

type readWriteDependencies struct {
	gate         runtime.Gate
	mutex        runtime.RepoMutex
	ipc          *ipcserver.Server
	activity     *activity.Journal
	reservations *reservationProjectionCoordinator
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
	for _, dir := range []string{stateDir, filepath.Join(wc, ".filees", "commit_cache"), filepath.Join(wc, ".filees", "passports"), filepath.Join(wc, ".filees", "tickets"), filepath.Join(wc, ".filees", "locks", "global"), filepath.Join(wc, ".filees", "locks", "repo"), logsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("init directory %s: %w", dir, err)
		}
	}
	if err := hideFileesDir(filepath.Join(wc, ".filees")); err != nil {
		logger.Warnf("hide .filees directory: %v", err)
	}
	// The folder gets the FileES icon in Explorer, so the owner can tell which
	// of his project directories are being kept and which are not without
	// opening anything. Best effort in both halves: a repository that syncs
	// perfectly must never fail to start because a decoration did.
	if iconPath, err := managedFolderIconPath(); err != nil {
		logger.Warnf("prepare folder icon: %v", err)
	} else if err := markManagedFolder(wc, iconPath); err != nil {
		logger.Warnf("mark managed folder: %v", err)
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
		manager, err = passport.Open(filepath.Join(wc, ".filees", "passports", "passports.json"), clientUUID, passport.SVNBackend{Client: svn, WC: wc}, passport.Config{TTL: repo.EditPassportTTL, HeartbeatInterval: repo.EditPassportHeartbeat, MaxSession: repo.EditPassportMaxSession, CloseGrace: repo.EditPassportCloseGrace, WorkingCopy: wc})
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
	runtimeRepo.state.SetWorkingCopySizeFunc(scanner.WorkingCopySize)
	sizeFuncWired := true
	rules := buildCommitRules(repo, latency)
	sink, err := openRepoErrorSink(filepath.Join(logsDir, "errors.jsonl"), "commit:"+repo.ID)
	if err != nil {
		logger.Warnf("structured errors disabled: %v", err)
		sink = nil
	}
	service := buildCommitService(repo, svn, rules, deps.gate, deps.mutex, clientUUID, sink, deps.ipc, runtimeRepo.state, manager, deps.activity)
	recoverReadWriteWorkingCopy(ctx, svn, wc, service, sink, logger)
	applyEditingPolicyMigration(ctx, repo, svn, wc, stateDir, clientUUID, manager != nil, sink, logger)
	if deps.reservations != nil {
		deps.reservations.AttachLocal(desired.Key, svn, wc, manager)
	}
	wireRepoLockFuncs(runtimeRepo.state, svn, wc, manager, func() {
		if deps.reservations != nil {
			deps.reservations.Schedule(desired.Key.ServerID)
		}
	})
	logger.Infof("reservation/lock listing wired (read-write)")
	runtimeRepo.state.SetPublishFunc(func(ctx context.Context, comment string) (int64, error) {
		revision, err := service.RequestPublish(ctx, wc, comment)
		if err == nil && deps.reservations != nil {
			deps.reservations.Schedule(desired.Key.ServerID)
		}
		return revision, err
	})
	runtimeRepo.state.SetNoticeFuncs(service.RecentNotices, service.AckNotice)
	lockFuncsWired := true
	defer func() {
		if sizeFuncWired {
			runtimeRepo.state.SetWorkingCopySizeFunc(nil)
		}
		if lockFuncsWired {
			runtimeRepo.state.SetLockFuncs(nil, nil)
			runtimeRepo.state.SetReservationReleaseFunc(nil)
			if deps.reservations != nil {
				deps.reservations.DetachLocal(desired.Key)
			}
			runtimeRepo.state.SetPublishFunc(nil)
			runtimeRepo.state.SetNoticeFuncs(nil, nil)
			logger.Infof("reservation/lock listing unwired (instance setup unwound)")
		}
	}()
	instance, err := reposupervisor.StartManaged(ctx, func(runCtx context.Context) error {
		return runReadWritePipeline(runCtx, repo, runtimeRepo.state, scanner, service)
	}, func(cleanupCtx context.Context) error {
		var first error
		runtimeRepo.state.SetWorkingCopySizeFunc(nil)
		runtimeRepo.state.SetLockFuncs(nil, nil)
		runtimeRepo.state.SetReservationReleaseFunc(nil)
		if deps.reservations != nil {
			deps.reservations.DetachLocal(desired.Key)
		}
		runtimeRepo.state.SetPublishFunc(nil)
		runtimeRepo.state.SetNoticeFuncs(nil, nil)
		logger.Infof("reservation/lock listing unwired (instance stopping)")
		if passports != nil {
			if err := passports.Stop(cleanupCtx); err != nil {
				first = err
			}
		}
		if err := os.Remove(pidPath); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
		return first
	})
	if err != nil {
		return nil, err
	}
	cleanupPID = false
	rollbackPassport = false
	lockFuncsWired = false
	sizeFuncWired = false
	_ = desired
	return instance, nil
}

func wireRepoLockFuncs(state *ipcserver.RepoState, svn client.Client, wc string, manager *passport.Manager, changed func()) {
	wireRepoReservationReleaseFunc(state, svn, wc, manager, changed)
	if manager != nil {
		state.SetLockFuncs(
			func(ctx context.Context, paths []string) (string, error) {
				// Manual tray "Wypożycz do edycji" - deliberately realm-unaware
				// (empty realmID): this is the non-owner borrow path
				// (AUTOLOCK_CREATOR_OWNERSHIP_CONCEPT_V2.md §3.2), never a
				// silent same-realm takeover.
				_, out, err := manager.Acquire(ctx, paths, "")
				if err == nil && changed != nil {
					changed()
				}
				return out, err
			},
			func(ctx context.Context, paths []string) (string, error) {
				out, err := manager.Release(ctx, paths)
				if err == nil && changed != nil {
					changed()
				}
				return out, err
			},
		)
		return
	}
	state.SetLockFuncs(
		func(ctx context.Context, paths []string) (string, error) {
			out, err := svn.Lock(ctx, wc, paths)
			if err == nil && changed != nil {
				changed()
			}
			return out, err
		},
		func(ctx context.Context, paths []string) (string, error) {
			out, err := svn.Unlock(ctx, wc, paths)
			if err == nil && changed != nil {
				changed()
			}
			return out, err
		},
	)
}

// wireRepoReservationFuncs supplies a live, server-contacting lock inventory
// for this working copy.  A reservation can only be released using the token
// returned by the immediately preceding list, and a passport-backed lock can
// only be released by the local passport manager that owns it.
func wireRepoReservationFuncs(state *ipcserver.RepoState, svn client.Client, wc string, manager *passport.Manager) {
	wireRepoReservationFuncsInternal(state, svn, wc, manager, true, nil)
}

func wireRepoReservationReleaseFunc(state *ipcserver.RepoState, svn client.Client, wc string, manager *passport.Manager, changed func()) {
	wireRepoReservationFuncsInternal(state, svn, wc, manager, false, changed)
}

func wireRepoReservationFuncsInternal(state *ipcserver.RepoState, svn client.Client, wc string, manager *passport.Manager, wireList bool, changed func()) {
	lister, ok := svn.(client.LockLister)
	if !ok || lister == nil {
		if wireList {
			state.SetReservationFuncs(nil, nil)
		} else {
			state.SetReservationReleaseFunc(nil)
		}
		return
	}
	// rawList is the historical, directly-querying-SVN-over-SSH shape.
	// TODO(reservation-resilience): the two-track scheduler that replaces
	// this with a call to the remote serving-state worker
	// (pkg/reservation/v1) is Codex's pion — see
	// concepts/RESERVATION_SERVER_EMISSION_WORKPLAN.md and
	// concepts/fixture_plan.md r680. This wrapper only adapts the return
	// shape to ipcserver.ReservationSnapshot so the package still compiles
	// against the new signature; it makes no freshness claim beyond what
	// SVN itself just answered (Stale/Unknown always false here).
	rawList := func(ctx context.Context) ([]contract.Reservation, error) {
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
			// A lock whose comment is not passport metadata predates the
			// editing policy on this repository - it was taken through the raw
			// SVN path that a free repository wires. No passport will ever
			// claim it, and every client authenticates as the same SVN
			// account, so its owner field cannot say whose it is either.
			// Withholding release would strand it forever; the release path
			// still demands explicit risk confirmation.
			_, isPassportLock := passport.ParseComment(entry.Comment)
			rows = append(rows, contract.Reservation{
				RepoID:         state.Summary().ID,
				WorkingCopy:    wc,
				Path:           filepath.ToSlash(entry.Path),
				Token:          entry.Token,
				OwnerID:        entry.Owner,
				CreatedAt:      entry.Created.UTC().Format(time.RFC3339Nano),
				CanRelease:     manager == nil || activePassport || !isPassportLock,
				LocalChanges:   reservationHasLocalChanges(entry),
				ActivePassport: activePassport,
			})
		}
		return rows, nil
	}
	list := func(ctx context.Context) (ipcserver.ReservationSnapshot, error) {
		rows, err := rawList(ctx)
		if err != nil {
			return ipcserver.ReservationSnapshot{}, err
		}
		return ipcserver.ReservationSnapshot{Reservations: rows}, nil
	}
	release := func(ctx context.Context, relativePath, expectedToken string, confirmRisk bool) error {
		rows, err := rawList(ctx)
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
		if manager != nil && row.ActivePassport {
			_, err := manager.Release(ctx, []string{absolute})
			if err == nil && changed != nil {
				changed()
			}
			return err
		}
		_, err = svn.Unlock(ctx, wc, []string{absolute})
		if err == nil && changed != nil {
			changed()
		}
		return err
	}
	if wireList {
		state.SetReservationFuncs(list, release)
	} else {
		state.SetReservationReleaseFunc(release)
	}
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
	retryInterval  time.Duration
	reservations   *reservationProjectionCoordinator
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
	// authority, endpoint and editing policy always come from the validated
	// server projection. The policy is deliberately not persisted alongside
	// the attachment: it is repository-wide and owner-controlled, so a second
	// stored copy could only ever go stale against the projection.
	runtime.config.Access = desired.Access
	runtime.config.RepoURL = desired.URL
	runtime.config.EditingPolicy = desired.EditingPolicy
	runtime.config.EditPassports = passportsRequired(desired.EditingPolicy)
	svn := s.newSVN(runtime.config)
	if err := validateAttachedWorkingCopy(startCtx, svn, runtime.config.LocalPath, desired); err != nil {
		return s.waitForWorkingCopy(runtime, svn, desired, err), nil
	}
	return s.startConfigured(s.daemonCtx, runtime, svn, desired)
}

// appliedEditingPolicyPath records which policy this working copy was last
// migrated to. It exists for the rollback direction only: svn:needs-lock is
// versioned, so leaving the repository is a content change that must happen
// exactly on the transition and never speculatively.
func appliedEditingPolicyPath(stateDir string) string {
	return filepath.Join(stateDir, "editing-policy")
}

func readAppliedEditingPolicy(stateDir string) string {
	raw, err := os.ReadFile(appliedEditingPolicyPath(stateDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// applyEditingPolicyMigration brings the working copy in line with the
// projected policy. It never fails repository start: a migration that cannot
// run right now leaves the repository working under its previous rules and
// says so, because the alternative - returning an error from startReadWrite -
// takes the repository down silently and removes the very access the user
// needs to clear the blockage.
func applyEditingPolicyMigration(ctx context.Context, repo config.Repo, svn client.Client, wc, stateDir, clientUUID string, passportsOn bool, sink *errmap.Sink, logger talk.Logger) {
	batch := intOrDefault(repo.MaxBatchFiles, 100)
	applied := readAppliedEditingPolicy(stateDir)

	if passportsOn {
		// Always run forward: it is idempotent and doubles as repair for
		// paths another client added without the property.
		skipped, err := passport.EnsureNeedsLock(ctx, svn, wc, clientUUID, batch)
		if err != nil {
			reportEditingPolicyBlocked(err, "włączenie", sink, logger)
			return
		}
		if skipped > 0 {
			reportEditingPolicyPartial(skipped, "włączenie", stateDir, logger)
			return
		}
		writeAppliedEditingPolicy(stateDir, clientview.EditingLockRequired, logger)
		return
	}

	// Rolling back only on a real transition matters for more than cost. The
	// mobile append channel sets svn:needs-lock on uploaded files on its own
	// (internal/mobileworker/svnappend.go), so a repository that never ran
	// this policy may carry the property legitimately, and clearing it
	// speculatively would destroy that.
	if applied != clientview.EditingLockRequired {
		return
	}
	skipped, err := passport.ClearNeedsLock(ctx, svn, wc, clientUUID, batch)
	if err != nil {
		reportEditingPolicyBlocked(err, "wyłączenie", sink, logger)
		return
	}
	if skipped > 0 {
		reportEditingPolicyPartial(skipped, "wyłączenie", stateDir, logger)
		return
	}
	writeAppliedEditingPolicy(stateDir, clientview.EditingFree, logger)
}

// reportEditingPolicyPartial records that some paths were left for a later
// pass because somebody is holding them. Deliberately not an error entry: the
// migration did as much as it legitimately could, and the remainder finishes
// on its own once the holds are released. Crucially it does not write the
// marker, since that would claim a migration that only partly happened and
// stop the retry from ever running.
func reportEditingPolicyPartial(skipped int, direction string, stateDir string, logger talk.Logger) {
	logger.Infof("%s polityki blokad częściowe: %d ścieżek jest wypożyczonych, dokończy się po ich zwolnieniu", direction, skipped)
}

func writeAppliedEditingPolicy(stateDir, policy string, logger talk.Logger) {
	// Written only after the migration it describes has committed, so a crash
	// mid-migration retries on the next start rather than claiming to be done.
	if err := os.WriteFile(appliedEditingPolicyPath(stateDir), []byte(policy+"\n"), 0o644); err != nil {
		logger.Warnf("persist applied editing policy: %v", err)
	}
}

func reportEditingPolicyBlocked(err error, direction string, sink *errmap.Sink, logger talk.Logger) {
	if errors.Is(err, passport.ErrWorkingCopyDirty) {
		logger.Warnf("%s polityki blokad odłożone: kopia robocza ma niezapisane zmiany (%v)", direction, err)
		if sink != nil {
			sink.Emit(errmap.Entry{
				Code:     errmap.CodePolicyDeferred,
				Key:      "policy.deferred",
				Severity: errmap.SevWarn,
				Hint:     errmap.HintRequireAction,
				Msg:      "Zmiana polityki blokad czeka na opublikowanie lokalnych zmian",
				Details:  err.Error(),
			})
		}
		return
	}
	logger.Warnf("%s polityki blokad nie powiodło się: %v", direction, err)
	if sink != nil {
		sink.Emit(errmap.Classify(err))
	}
}

// passportsRequired maps the projected repository policy onto the runtime
// switch that builds the passport manager. Only the canonical opted-in value
// enables it: an unset policy, and equally a value this build does not
// recognise, leave the repository on plain merge-on-commit rather than
// half-enabling a barrier the client would not know how to satisfy.
func passportsRequired(policy string) bool { return policy == clientview.EditingLockRequired }

func validateAttachedWorkingCopy(ctx context.Context, svn client.Client, workingCopy string, desired reposupervisor.Desired) error {
	if _, err := os.Stat(filepath.Join(workingCopy, ".svn")); err != nil {
		return fmt.Errorf("working copy metadata is missing: %w", err)
	}
	info, err := svn.GetInfo(ctx, workingCopy)
	if err != nil {
		return fmt.Errorf("working copy is invalid: %w", err)
	}
	if !infoHasURL(info, desired.URL) {
		return errors.New("working copy URL does not match projected repository")
	}
	return validateWorkingCopyIdentity(workingCopy, expectedWorkingCopyIdentity(desired.Key.ServerID, desired.Key.RepoID, desired.URL))
}

func (s *daemonRepoStarter) startConfigured(lifecycle context.Context, runtime repoRuntime, svn client.Client, desired reposupervisor.Desired) (reposupervisor.Instance, error) {
	if err := ensureWorkingCopyIdentity(runtime.config.LocalPath, expectedWorkingCopyIdentity(desired.Key.ServerID, desired.Key.RepoID, desired.URL)); err != nil {
		return nil, err
	}
	guard, err := acquireWorkingCopyGuard(runtime.config.LocalPath)
	if err != nil {
		return nil, err
	}
	var instance reposupervisor.Instance
	if desired.Access == contract.AccessReadWrite {
		if s.startReadWrite == nil {
			_ = guard.Close()
			return nil, errors.New("read-write pipeline factory is not connected yet")
		}
		instance, err = s.startReadWrite(lifecycle, runtime, svn, desired)
	} else if desired.Access == contract.AccessReadOnly {
		instance, err = s.startReadOnly(lifecycle, runtime, svn, desired)
	} else {
		err = errors.New("repository access is invalid")
	}
	if err != nil {
		_ = guard.Close()
		return nil, err
	}
	return &guardedRepoInstance{inner: instance, guard: guard}, nil
}

func (s *daemonRepoStarter) startReadOnly(lifecycle context.Context, runtime repoRuntime, svn client.Client, desired reposupervisor.Desired) (reposupervisor.Instance, error) {
	logger := talk.With("readonly:" + desired.Key.String())
	// Read-only attachments contribute to the server-menu inventory as well,
	// but intentionally receive no release callback.
	if s.reservations != nil {
		s.reservations.AttachLocal(desired.Key, svn, runtime.config.LocalPath, nil)
	}
	logger.Infof("reservation local overlay wired (read-only)")
	wc := runtime.config.LocalPath
	runtime.state.SetNoticeFuncs(
		func() ([]contract.Notice, error) { return shout.RecentNotices(wc, 20) },
		func(id string) error { return shout.Ack(wc, id) },
	)
	stateDir := filepath.Join(runtime.config.LocalPath, ".filees", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	pidPath := filepath.Join(stateDir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		return nil, err
	}
	logsDir := filepath.Join(wc, ".filees", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return nil, err
	}
	sink, err := openRepoErrorSink(filepath.Join(logsDir, "errors.jsonl"), "sync:"+runtime.config.ID)
	if err != nil {
		logger.Warnf("structured errors disabled: %v", err)
		sink = nil
	}
	return reposupervisor.StartManaged(lifecycle, func(ctx context.Context) error {
		runReadOnlyRepo(ctx, runtime.config, runtime.state, svn, sink, logger)
		return nil
	}, func(context.Context) error {
		runtime.state.SetReservationReleaseFunc(nil)
		if s.reservations != nil {
			s.reservations.DetachLocal(desired.Key)
		}
		runtime.state.SetNoticeFuncs(nil, nil)
		logger.Infof("reservation listing unwired (instance stopping)")
		if err := os.Remove(pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

type guardedRepoInstance struct {
	inner reposupervisor.Instance
	guard workingCopyGuard
}

func (instance *guardedRepoInstance) Stop(ctx context.Context) error {
	var stopErr error
	if instance.inner != nil {
		stopErr = instance.inner.Stop(ctx)
	}
	return errors.Join(stopErr, instance.guard.Close())
}

type waitingWorkingCopyInstance struct {
	cancel context.CancelFunc
	done   chan struct{}
	state  *ipcserver.RepoState

	mu    sync.Mutex
	inner reposupervisor.Instance
}

func (instance *waitingWorkingCopyInstance) Stop(ctx context.Context) error {
	instance.cancel()
	select {
	case <-instance.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	instance.mu.Lock()
	inner := instance.inner
	instance.mu.Unlock()
	instance.state.SetCurrentOp(nil)
	if inner != nil {
		return inner.Stop(ctx)
	}
	instance.state.SetState(contract.StateStopping)
	return nil
}

func (s *daemonRepoStarter) waitForWorkingCopy(runtime repoRuntime, svn client.Client, desired reposupervisor.Desired, initialErr error) reposupervisor.Instance {
	ctx, cancel := context.WithCancel(s.daemonCtx)
	instance := &waitingWorkingCopyInstance{cancel: cancel, done: make(chan struct{}), state: runtime.state}
	runtime.state.SetConnectivity(contract.ConnOnline)
	runtime.state.SetCurrentOp(stringPtr("working_copy_missing"))
	runtime.state.SetState(contract.StateInteractionRequired)
	logger := talk.With("repo:" + runtime.config.ID)
	logger.Warnf("working copy unavailable at %s: %v", runtime.config.LocalPath, initialErr)
	interval := s.retryInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	go func() {
		defer close(instance.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if err := validateAttachedWorkingCopy(ctx, svn, runtime.config.LocalPath, desired); err != nil {
				continue
			}
			runtime.state.SetCurrentOp(nil)
			inner, err := s.startConfigured(ctx, runtime, svn, desired)
			if err != nil {
				runtime.state.SetCurrentOp(stringPtr("working_copy_missing"))
				runtime.state.SetState(contract.StateDegraded)
				logger.Warnf("working copy returned but pipeline start failed: %v", err)
				continue
			}
			instance.mu.Lock()
			instance.inner = inner
			instance.mu.Unlock()
			logger.Infof("working copy restored at %s", runtime.config.LocalPath)
			return
		}
	}()
	return instance
}
