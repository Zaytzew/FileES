package clientupdate

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/internal/releaseenvelope"
)

type countingFetcher struct {
	artifactFetcher
	calls int
}

func (fetcher *countingFetcher) Cat(ctx context.Context, path string) ([]byte, error) {
	fetcher.calls++
	return fetcher.artifactFetcher.Cat(ctx, path)
}

// The owner's station, 2026-09-25: the plan fetched the whole bundle inside a
// 30-second call and failed on a slow link; the installation would have
// fetched it again. One verified download now serves both.
func TestBundleDownloadIsVerifiedKeptAndReused(t *testing.T) {
	bundle := makeBundle(t, tarEntry{name: "VERSION", typeflag: tar.TypeReg, mode: 0o644, data: "1.1\n"})
	resolved := stagedRelease(bundle)
	fetcher := &countingFetcher{artifactFetcher: artifactFetcher{"releases/r1/desktop/linux-amd64/client.tar.gz": bundle}}
	root := t.TempDir()
	stager := BundleStager{Fetcher: fetcher, Root: root}
	if _, _, ready := stager.DownloadProgress(resolved); ready {
		t.Fatal("ready before any download")
	}
	stale := filepath.Join(root, "downloads", strings.Repeat("0", 64)+".tar.gz")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("older release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := stager.Download(context.Background(), resolved); err != nil {
		t.Fatal(err)
	}
	if have, total, ready := stager.DownloadProgress(resolved); !ready || have != total || total != int64(len(bundle)) {
		t.Fatalf("progress = %d/%d ready=%v", have, total, ready)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("older release's download survived: %v", err)
	}
	staged, err := stager.Stage(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	staged.Remove()
	if staged, err = stager.Stage(context.Background(), resolved); err != nil {
		t.Fatal(err)
	}
	staged.Remove()
	if fetcher.calls != 1 {
		t.Fatalf("bundle fetched %d times", fetcher.calls)
	}

	corrupt := append([]byte(nil), bundle...)
	corrupt[len(corrupt)/2] ^= 1
	other := BundleStager{Fetcher: artifactFetcher{"releases/r1/desktop/linux-amd64/client.tar.gz": corrupt}, Root: t.TempDir()}
	if _, err := other.Download(context.Background(), resolved); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("corrupt download: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(other.Root, "downloads")); len(entries) != 0 {
		t.Fatalf("corrupt bytes kept: %v", entries)
	}
}

type downloadingInstaller struct {
	installerStub
	mu        sync.Mutex
	release   chan struct{}
	fail      error
	downloads int
	have      int64
	ready     bool
}

func (d *downloadingInstaller) Download(ctx context.Context, _ *releaseenvelope.Resolved) error {
	d.mu.Lock()
	d.downloads++
	d.mu.Unlock()
	select {
	case <-d.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail != nil {
		return d.fail
	}
	d.have, d.ready = 10, true
	return nil
}

func (d *downloadingInstaller) DownloadProgress(*releaseenvelope.Resolved) (int64, int64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.have, 10, d.ready
}

func waitForDownload(t *testing.T, service *Service) {
	t.Helper()
	service.downloadMu.Lock()
	job := service.download
	service.downloadMu.Unlock()
	if job == nil {
		t.Fatal("no download was started")
	}
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("download did not finish")
	}
}

func TestUpdateIsDownloadedInTheBackgroundAndThePlanDoesNotWaitForIt(t *testing.T) {
	now := time.Date(2026, 9, 25, 20, 0, 0, 0, time.UTC)
	resolver := &countingResolver{resolved: resolvedRelease(2, "r2", "1.1")}
	installer := &downloadingInstaller{release: make(chan struct{})}
	service := &Service{Resolver: resolver, Installer: installer, State: StateStore{Path: filepath.Join(t.TempDir(), "update.json")}, CurrentVersion: "1.0", Now: func() time.Time { return now }}

	status, err := service.Status(context.Background())
	if err != nil || status.State != "available" || status.Download != "downloading" || status.DownloadTotal != 10 {
		t.Fatalf("status = %+v, %v", status, err)
	}
	installer.mu.Lock()
	installer.have = 4
	installer.mu.Unlock()
	var downloading *DownloadingError
	if _, err := service.Plan(context.Background()); !errors.As(err, &downloading) || downloading.Have != 4 || downloading.Total != 10 {
		t.Fatalf("plan during the download: %v", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("plan during the download read the channel (%d reads)", resolver.calls)
	}
	close(installer.release)
	waitForDownload(t, service)
	if status, _ := service.Status(context.Background()); status.Download != "ready" || status.DownloadedBytes != 10 {
		t.Fatalf("status after download = %+v", status)
	}
	plan, err := service.Plan(context.Background())
	if err != nil || plan.AvailableVersion != "1.1" || installer.planCalls != 1 {
		t.Fatalf("plan = %+v, %v (installer plans %d)", plan, err, installer.planCalls)
	}
	if resolver.calls != 1 || installer.downloads != 1 {
		t.Fatalf("after the download: channel reads %d, downloads %d", resolver.calls, installer.downloads)
	}
}

func TestFailedDownloadIsReportedToThePlanAndRetriedLater(t *testing.T) {
	now := time.Date(2026, 9, 25, 20, 0, 0, 0, time.UTC)
	resolver := &countingResolver{resolved: resolvedRelease(2, "r2", "1.1")}
	installer := &downloadingInstaller{release: make(chan struct{}), fail: errors.New("link dropped")}
	close(installer.release)
	service := &Service{Resolver: resolver, Installer: installer, State: StateStore{Path: filepath.Join(t.TempDir(), "update.json")}, CurrentVersion: "1.0", Now: func() time.Time { return now }}
	if _, err := service.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, service)
	if status, _ := service.Status(context.Background()); status.Download != "failed" || !strings.Contains(status.DownloadError, "link dropped") {
		t.Fatalf("status = %+v", status)
	}
	var downloading *DownloadingError
	if _, err := service.Plan(context.Background()); err == nil || errors.As(err, &downloading) || !strings.Contains(err.Error(), "link dropped") {
		t.Fatalf("plan after a failed download: %v", err)
	}
	if installer.downloads != 1 {
		t.Fatalf("failure retried at once: %d downloads", installer.downloads)
	}
	now = now.Add(downloadRetryAfter)
	if _, err := service.Plan(context.Background()); !errors.As(err, &downloading) {
		t.Fatalf("plan after the retry interval: %v", err)
	}
	waitForDownload(t, service)
	if installer.downloads != 2 {
		t.Fatalf("downloads = %d", installer.downloads)
	}
}
