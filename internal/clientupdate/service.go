package clientupdate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"filees/internal/releaseenvelope"
	contract "filees/pkg/contract/v1"
)

type Resolver interface {
	Resolve(context.Context, string, string, string) (*releaseenvelope.Resolved, error)
}

type Installer interface {
	Plan(context.Context, *releaseenvelope.Resolved) ([]contract.UpdateChange, bool, error)
	Apply(context.Context, *releaseenvelope.Resolved) error
}

type Service struct {
	Resolver       Resolver
	Installer      Installer
	State          StateStore
	Channel        string
	ChannelPath    string
	Component      string
	Platform       string
	CurrentVersion string

	mu sync.Mutex
	// appliedVersion bridges the short interval after an update replaces the
	// files but before the supervisor restarts this still-running process.
	appliedVersion   string
	appliedReleaseID string
	appliedRestart   bool

	// The last verified status. repo and system status answers carry it, and
	// the interface asks for those after every event: reading the channel
	// (four remote reads) each time, behind mu, stacked the calls past the
	// interface's 10-second limit whenever the link to the release server
	// slowed down, and the interface showed the daemon as gone, in a cycle
	// (owner's production, 2026-09-25). Errors are never cached.
	cacheMu  sync.Mutex
	cached   *contract.UpdateStatus
	cachedAt time.Time
	Now      func() time.Time

	// The available release's bundle, fetched in the background (Downloader).
	downloadMu sync.Mutex
	download   *downloadJob
}

// Downloader is implemented by installers whose bundle can be fetched ahead
// of the plan and the installation.
type Downloader interface {
	Download(context.Context, *releaseenvelope.Resolved) error
	DownloadProgress(*releaseenvelope.Resolved) (have, total int64, ready bool)
}

type downloadJob struct {
	resolved *releaseenvelope.Resolved
	running  bool
	err      error
	finished time.Time
	done     chan struct{}
}

// DownloadingError answers a plan asked for while the bundle is still being
// fetched: at once, with the progress, instead of after the network.
type DownloadingError struct{ Have, Total int64 }

func (e *DownloadingError) Error() string {
	return fmt.Sprintf("update bundle is still downloading: %d of %d bytes", e.Have, e.Total)
}

func (e *DownloadingError) DownloadProgress() (int64, int64) { return e.Have, e.Total }

const (
	// downloadTimeout bounds one background fetch; downloadRetryAfter spaces
	// automatic retries after a failure (a user's plan request retries at once).
	downloadTimeout    = 30 * time.Minute
	downloadRetryAfter = time.Minute
	// A resolution made by a successful download stays good for planning this
	// long, so the plan does not read the channel over the network again.
	downloadResolvedTTL = 30 * time.Minute
)

func sameRelease(left, right *releaseenvelope.Resolved) bool {
	return left != nil && right != nil && left.Envelope.ReleaseID == right.Envelope.ReleaseID && left.Manifest.Version == right.Manifest.Version
}

// ensureDownload starts fetching resolved's bundle unless it is already being
// fetched or is on disk. A failed fetch is retried automatically only after
// downloadRetryAfter; now retries at once.
func (service *Service) ensureDownload(resolved *releaseenvelope.Resolved, now bool) {
	downloader, ok := service.Installer.(Downloader)
	if !ok || resolved == nil {
		return
	}
	service.downloadMu.Lock()
	defer service.downloadMu.Unlock()
	if job := service.download; job != nil && sameRelease(job.resolved, resolved) {
		if job.running || job.err == nil || (!now && service.now().Sub(job.finished) < downloadRetryAfter) {
			return
		}
	}
	job := &downloadJob{resolved: resolved, running: true, done: make(chan struct{})}
	service.download = job
	go func() {
		defer close(job.done)
		ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
		err := downloader.Download(ctx, resolved)
		cancel()
		service.downloadMu.Lock()
		job.running, job.err, job.finished = false, err, service.now()
		service.downloadMu.Unlock()
	}()
}

func (service *Service) currentDownload() (downloadJob, bool) {
	service.downloadMu.Lock()
	defer service.downloadMu.Unlock()
	if service.download == nil {
		return downloadJob{}, false
	}
	return *service.download, true
}

// withDownload adds the live download state to an available status. It is
// applied to cached answers too: the cache holds the channel, not the progress.
func (service *Service) withDownload(status contract.UpdateStatus) contract.UpdateStatus {
	downloader, ok := service.Installer.(Downloader)
	job, started := service.currentDownload()
	if !ok || !started || status.State != "available" || job.resolved.Manifest.Version != status.AvailableVersion {
		return status
	}
	if !job.running && job.err != nil && service.now().Sub(job.finished) >= downloadRetryAfter {
		service.ensureDownload(job.resolved, false)
		job, _ = service.currentDownload()
	}
	have, total, ready := downloader.DownloadProgress(job.resolved)
	status.DownloadedBytes, status.DownloadTotal = have, total
	switch {
	case ready:
		status.Download = "ready"
	case job.running:
		status.Download = "downloading"
	case job.err != nil:
		status.Download, status.DownloadError = "failed", job.err.Error()
	}
	return status
}

// statusCacheTTL bounds how old a verified channel answer may be before a
// status call reads the channel again.
const statusCacheTTL = 5 * time.Minute

func (service *Service) now() time.Time {
	if service.Now != nil {
		return service.Now()
	}
	return time.Now()
}

func (service *Service) cachedStatus(fresh bool) (contract.UpdateStatus, bool) {
	service.cacheMu.Lock()
	defer service.cacheMu.Unlock()
	if service.cached == nil || (fresh && service.now().Sub(service.cachedAt) >= statusCacheTTL) {
		return contract.UpdateStatus{}, false
	}
	return *service.cached, true
}

func (service *Service) storeStatus(status *contract.UpdateStatus) {
	service.cacheMu.Lock()
	defer service.cacheMu.Unlock()
	service.cached, service.cachedAt = status, service.now()
}

func (service *Service) Status(ctx context.Context) (contract.UpdateStatus, error) {
	if status, ok := service.cachedStatus(true); ok {
		return service.withDownload(status), nil
	}
	// Another check, a plan or an installation holds the channel: answer with
	// the last verified status instead of queueing behind the network.
	if !service.mu.TryLock() {
		if status, ok := service.cachedStatus(false); ok {
			return service.withDownload(status), nil
		}
		return contract.UpdateStatus{}, errors.New("update status is being checked")
	}
	defer service.mu.Unlock()
	if service.appliedRestart {
		return contract.UpdateStatus{
			State: "restart_required", Channel: service.Channel,
			CurrentVersion:   canonicalClientVersion(service.CurrentVersion),
			AvailableVersion: service.appliedVersion, ReleaseID: service.appliedReleaseID,
			Summary:         "Aktualizacja jest zainstalowana. Uruchom FileES ponownie, aby używać nowej wersji.",
			RestartRequired: true,
		}, nil
	}
	resolved, state, err := service.resolve(ctx)
	if err != nil {
		return contract.UpdateStatus{}, err
	}
	current := service.currentVersion(state)
	status := contract.UpdateStatus{
		State: "current", Channel: service.Channel, CurrentVersion: current, ReleaseID: resolved.Envelope.ReleaseID,
		Summary: fmt.Sprintf("Podpisane wydanie %s, sequence %d", resolved.SigningKeyID, resolved.Envelope.Sequence),
	}
	if clientUpdateAvailable(resolved.Manifest.Version, current) {
		status.State = "available"
		status.AvailableVersion = resolved.Manifest.Version
		status.RestartRequired = true
		service.ensureDownload(resolved, false)
	}
	service.storeStatus(&status)
	return service.withDownload(status), nil
}

// planResolution reuses the resolution of a completed download when it is
// recent, so a plan asked for after the background fetch reads nothing over
// the network. The anti-rollback state is still checked against it.
func (service *Service) planResolution(ctx context.Context) (*releaseenvelope.Resolved, State, error) {
	if job, ok := service.currentDownload(); ok && !job.running && job.err == nil && service.now().Sub(job.finished) < downloadResolvedTTL {
		state, err := service.State.Load()
		if err != nil {
			return nil, State{}, err
		}
		if err := state.Check(job.resolved.Envelope); err == nil {
			return job.resolved, state, nil
		}
	}
	return service.resolve(ctx)
}

func (service *Service) Plan(ctx context.Context) (contract.UpdatePlanResult, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.appliedRestart {
		return contract.UpdatePlanResult{
			CurrentVersion:   canonicalClientVersion(service.CurrentVersion),
			AvailableVersion: service.appliedVersion, ReleaseID: service.appliedReleaseID,
			RestartRequired: true,
		}, nil
	}
	if job, ok := service.currentDownload(); ok && job.running {
		have, total, _ := service.Installer.(Downloader).DownloadProgress(job.resolved)
		return contract.UpdatePlanResult{}, &DownloadingError{Have: have, Total: total}
	}
	resolved, state, err := service.planResolution(ctx)
	if err != nil {
		return contract.UpdatePlanResult{}, err
	}
	current := service.currentVersion(state)
	if !clientUpdateAvailable(resolved.Manifest.Version, current) {
		return contract.UpdatePlanResult{
			CurrentVersion: current, AvailableVersion: resolved.Manifest.Version,
			ReleaseID: resolved.Envelope.ReleaseID,
		}, nil
	}
	if downloader, ok := service.Installer.(Downloader); ok {
		if have, total, ready := downloader.DownloadProgress(resolved); !ready {
			// A fetch of this release that has just failed is reported, not
			// restarted: whoever waits on the plan must see the failure.
			if job, ok := service.currentDownload(); ok && sameRelease(job.resolved, resolved) && job.err != nil && service.now().Sub(job.finished) < downloadRetryAfter {
				return contract.UpdatePlanResult{}, fmt.Errorf("download update bundle: %w", job.err)
			}
			service.ensureDownload(resolved, true)
			return contract.UpdatePlanResult{}, &DownloadingError{Have: have, Total: total}
		}
	}
	changes, restart, err := service.Installer.Plan(ctx, resolved)
	if err != nil {
		return contract.UpdatePlanResult{}, err
	}
	return contract.UpdatePlanResult{
		CurrentVersion: current, AvailableVersion: resolved.Manifest.Version,
		ReleaseID: resolved.Envelope.ReleaseID, Changes: changes, RestartRequired: restart,
	}, nil
}

func (service *Service) Apply(ctx context.Context) (contract.UpdateApplyResult, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.appliedRestart {
		return contract.UpdateApplyResult{InstalledVersion: service.appliedVersion, RestartRequired: true}, nil
	}
	resolved, state, err := service.resolve(ctx)
	if err != nil {
		return contract.UpdateApplyResult{}, err
	}
	current := service.currentVersion(state)
	if !clientUpdateAvailable(resolved.Manifest.Version, current) {
		return contract.UpdateApplyResult{InstalledVersion: current}, nil
	}
	_, restart, err := service.Installer.Plan(ctx, resolved)
	if err != nil {
		return contract.UpdateApplyResult{}, fmt.Errorf("refresh update plan: %w", err)
	}
	if err := service.Installer.Apply(ctx, resolved); err != nil {
		return contract.UpdateApplyResult{}, err
	}
	// The files have changed even if persisting the anti-rollback checkpoint
	// subsequently fails. Keep the restart latch for this process lifetime;
	// GUI reconnects and an unavailable release server must not erase it.
	service.appliedVersion = resolved.Manifest.Version
	service.appliedReleaseID = resolved.Envelope.ReleaseID
	service.appliedRestart = restart
	service.storeStatus(nil) // the installed files changed; verify again
	next, err := state.Advance(resolved.Envelope, resolved.Manifest.Version)
	if err != nil {
		return contract.UpdateApplyResult{}, err
	}
	if err := service.State.Save(next); err != nil {
		return contract.UpdateApplyResult{}, fmt.Errorf("persist update high-water mark: %w", err)
	}
	return contract.UpdateApplyResult{InstalledVersion: resolved.Manifest.Version, RestartRequired: restart}, nil
}

func (service *Service) resolve(ctx context.Context) (*releaseenvelope.Resolved, State, error) {
	if service.Resolver == nil || service.Installer == nil {
		return nil, State{}, errors.New("client update service is incomplete")
	}
	state, err := service.State.Load()
	if err != nil {
		return nil, State{}, err
	}
	resolved, err := service.Resolver.Resolve(ctx, service.ChannelPath, service.Component, service.Platform)
	if err != nil {
		return nil, State{}, err
	}
	if err := state.Check(resolved.Envelope); err != nil {
		return nil, State{}, err
	}
	return resolved, state, nil
}

func (service *Service) currentVersion(state State) string {
	if service.appliedVersion != "" {
		return canonicalClientVersion(service.appliedVersion)
	}
	// The binary that is actually running wins over persisted history. This is
	// what lets the updater repair an old MSI installed over a newer channel
	// release without lowering the anti-rollback high-water mark.
	if service.CurrentVersion != "" {
		return canonicalClientVersion(service.CurrentVersion)
	}
	if state.InstalledVersion != "" {
		return canonicalClientVersion(state.InstalledVersion)
	}
	return ""
}

func sameClientVersion(left, right string) bool {
	return canonicalClientVersion(left) == canonicalClientVersion(right)
}

// clientUpdateAvailable keeps a locally newer client from being offered (or
// applying) an older signed channel release. This matters for acceptance
// builds made from a revision ahead of the currently promoted bundle: a valid
// signature authenticates a release, but does not make it newer than the
// binary which is already running.
//
// FileES distribution versions are dot-separated numeric components. Unknown
// schemes retain the previous mismatch-means-update behaviour instead of being
// ordered by a guess.
func clientUpdateAvailable(available, current string) bool {
	if sameClientVersion(available, current) {
		return false
	}
	comparison, comparable := compareNumericClientVersions(available, current)
	if !comparable {
		return true
	}
	return comparison > 0
}

func compareNumericClientVersions(left, right string) (int, bool) {
	leftParts, leftOK := numericClientVersionParts(left)
	rightParts, rightOK := numericClientVersionParts(right)
	if !leftOK || !rightOK {
		return 0, false
	}
	count := len(leftParts)
	if len(rightParts) > count {
		count = len(rightParts)
	}
	for index := 0; index < count; index++ {
		leftPart, rightPart := "0", "0"
		if index < len(leftParts) {
			leftPart = leftParts[index]
		}
		if index < len(rightParts) {
			rightPart = rightParts[index]
		}
		if len(leftPart) < len(rightPart) {
			return -1, true
		}
		if len(leftPart) > len(rightPart) {
			return 1, true
		}
		if leftPart < rightPart {
			return -1, true
		}
		if leftPart > rightPart {
			return 1, true
		}
	}
	return 0, true
}

func numericClientVersionParts(value string) ([]string, bool) {
	parts := strings.Split(canonicalClientVersion(value), ".")
	for index, part := range parts {
		if part == "" {
			return nil, false
		}
		for _, character := range part {
			if !unicode.IsDigit(character) || character > unicode.MaxASCII {
				return nil, false
			}
		}
		part = strings.TrimLeft(part, "0")
		if part == "" {
			part = "0"
		}
		parts[index] = part
	}
	return parts, true
}

// canonicalClientVersion maps the human-facing build stamp 0.1.15+r850 onto
// the numeric distribution/MSI form 0.1.15.850. Other version schemes remain
// untouched rather than being guessed at.
func canonicalClientVersion(value string) string {
	value = strings.TrimSpace(value)
	marker := strings.LastIndex(value, "+r")
	if marker <= 0 || marker+2 == len(value) {
		return value
	}
	for _, character := range value[marker+2:] {
		if !unicode.IsDigit(character) || character > unicode.MaxASCII {
			return value
		}
	}
	return value[:marker] + "." + value[marker+2:]
}
