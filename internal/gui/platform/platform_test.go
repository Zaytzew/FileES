package platform_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
)

func TestFailureClassificationAndCause(t *testing.T) {
	cause := errors.New("desktop service missing")
	err := platform.NewUnavailable("notifications", cause)
	if !platform.IsFailure(err, platform.FailureUnavailable) {
		t.Fatalf("error not classified as unavailable: %v", err)
	}
	if platform.IsFailure(err, platform.FailureOperational) {
		t.Fatalf("error classified as operational: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("cause not preserved: %v", err)
	}
}

func TestFakeRecordsCallsAndReturnsConfiguredResults(t *testing.T) {
	wantPaths := []string{"/wc/a.dwg", "/wc/b.dwg"}
	fake := &platformtest.Fake{
		PickFilesFunc: func(ctx context.Context, request platform.PickFilesRequest) (platform.PickFilesResult, error) {
			if err := ctx.Err(); err != nil {
				return platform.PickFilesResult{}, err
			}
			return platform.PickFilesResult{Paths: wantPaths}, nil
		},
		AutostartStatusFunc: func(context.Context, platform.AutostartSpec) (platform.AutostartState, error) {
			return platform.AutostartState{Enabled: true, Source: "test"}, nil
		},
	}

	ctx := context.Background()
	request := platform.PickFilesRequest{Title: "Lock", Root: "/wc", InitialDir: "/wc", AllowMultiple: true}
	result, err := fake.PickFiles(ctx, request)
	if err != nil || len(result.Paths) != 2 || result.Cancelled {
		t.Fatalf("PickFiles() = %#v, %v", result, err)
	}
	if err := fake.OpenFolder(ctx, "/wc"); err != nil {
		t.Fatal(err)
	}
	notification := platform.Notification{ID: "offline", Group: "repo", Title: "FileES", Urgency: platform.UrgencyNormal}
	if err := fake.Notify(ctx, notification); err != nil {
		t.Fatal(err)
	}
	spec := platform.AutostartSpec{ID: "filees", Name: "FileES", Executable: "/usr/bin/filees-gui"}
	state, err := fake.AutostartStatus(ctx, spec)
	if err != nil || !state.Enabled {
		t.Fatalf("AutostartStatus() = %#v, %v", state, err)
	}
	if err := fake.SetAutostart(ctx, spec, true); err != nil {
		t.Fatal(err)
	}

	snapshot := fake.Snapshot()
	if len(snapshot.OpenedFolders) != 1 || snapshot.OpenedFolders[0] != "/wc" {
		t.Fatalf("opened folders = %#v", snapshot.OpenedFolders)
	}
	if len(snapshot.PickRequests) != 1 || snapshot.PickRequests[0] != request {
		t.Fatalf("pick requests = %#v", snapshot.PickRequests)
	}
	if len(snapshot.Notifications) != 1 || snapshot.Notifications[0] != notification {
		t.Fatalf("notifications = %#v", snapshot.Notifications)
	}
	if len(snapshot.StatusRequests) != 1 || len(snapshot.AutostartSets) != 1 || !snapshot.AutostartSets[0].Enabled {
		t.Fatalf("autostart calls = %#v / %#v", snapshot.StatusRequests, snapshot.AutostartSets)
	}
}

func TestFakePickerDefaultsToUserCancellation(t *testing.T) {
	result, err := (&platformtest.Fake{}).PickFiles(context.Background(), platform.PickFilesRequest{})
	if err != nil || !result.Cancelled || len(result.Paths) != 0 {
		t.Fatalf("PickFiles() = %#v, %v", result, err)
	}
}

func TestFakePropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &platformtest.Fake{OpenFolderFunc: func(ctx context.Context, _ string) error {
		return ctx.Err()
	}}
	if err := fake.OpenFolder(ctx, "/wc"); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenFolder() error = %v", err)
	}
}

func TestFakeRecordingIsConcurrentSafe(t *testing.T) {
	const calls = 50
	fake := &platformtest.Fake{}
	var wg sync.WaitGroup
	wg.Add(calls)
	for i := 0; i < calls; i++ {
		go func() {
			defer wg.Done()
			_ = fake.Notify(context.Background(), platform.Notification{ID: "event"})
			_ = fake.Snapshot()
		}()
	}
	wg.Wait()
	if got := len(fake.Snapshot().Notifications); got != calls {
		t.Fatalf("notifications = %d, want %d", got, calls)
	}
}
