package actions

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
)

type fakeProgress struct {
	request platform.ProgressRequest
	calls   int32
	closed  int32
	err     error
	nilFunc bool
}

func (f *fakeProgress) ShowProgress(_ context.Context, request platform.ProgressRequest) (func(), error) {
	atomic.AddInt32(&f.calls, 1)
	f.request = request
	if f.err != nil {
		return nil, f.err
	}
	if f.nilFunc {
		return nil, nil
	}
	return func() { atomic.AddInt32(&f.closed, 1) }, nil
}

// The progress window explains a wait; it never gates anything. Every way it
// can be absent or broken must therefore yield a callable no-op, because the
// call sites `defer` the result without a nil check and an import must not be
// taken down by a missing zenity.
func TestShowProgressAlwaysReturnsACallableCloser(t *testing.T) {
	cases := map[string]Config{
		"no presenter configured": {},
		"presenter fails":         {Progress: &fakeProgress{err: errors.New("zenity is not installed")}},
		"presenter returns nil":   {Progress: &fakeProgress{nilFunc: true}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			controller := &Controller{cfg: cfg}
			close := controller.showProgress(context.Background(), "T", "B")
			if close == nil {
				t.Fatal("showProgress returned a nil closer")
			}
			close() // must not panic
		})
	}
}

// The window must outlive the lifecycle poll: "attached" only binds the working
// copy, while the initial import keeps pushing. Busy is read exactly as the
// tray reads it, so window and clock icon cannot disagree.
func TestAwaitRepositorySettledWaitsWhileTheTrayWouldShowBusy(t *testing.T) {
	const path = `E:\CLOUD-NEO\JANCZEWICE`
	op := "commit"
	states := []app.ViewModel{
		{}, // repo not projected yet — must count as busy, not as done
		{Repos: []app.RepoViewModel{{LocalPath: path, State: contract.StateAttaching}}},
		{Repos: []app.RepoViewModel{{LocalPath: path, State: contract.StateActive, CurrentOp: &op}}},
		{Repos: []app.RepoViewModel{{LocalPath: path, State: contract.StateActive}}},
	}
	var reads int32
	controller := &Controller{cfg: Config{
		CreationStatusPollTimeout: 5 * time.Second,
		ViewModel: func() app.ViewModel {
			index := int(atomic.AddInt32(&reads, 1)) - 1
			if index >= len(states) {
				index = len(states) - 1
			}
			return states[index]
		},
	}}

	done := make(chan struct{})
	go func() { defer close(done); controller.awaitRepositorySettled(context.Background(), path) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("awaitRepositorySettled never returned")
	}
	if got := atomic.LoadInt32(&reads); got < int32(len(states)) {
		t.Fatalf("settled after %d reads; it stopped before the repo was idle", got)
	}
}

// An unknown path, a stalled daemon or a missing ViewModel must never pin a
// window on screen: the wait is bounded by the same timeout as the lifecycle
// poll behind it.
func TestAwaitRepositorySettledIsBounded(t *testing.T) {
	controller := &Controller{cfg: Config{
		CreationStatusPollTimeout: 50 * time.Millisecond,
		ViewModel:                 func() app.ViewModel { return app.ViewModel{} },
	}}
	done := make(chan struct{})
	go func() { defer close(done); controller.awaitRepositorySettled(context.Background(), `E:\nieznany`) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a permanently busy repository pinned the window")
	}
}

func TestAwaitRepositorySettledStopsWithTheContext(t *testing.T) {
	controller := &Controller{cfg: Config{
		CreationStatusPollTimeout: time.Hour,
		ViewModel:                 func() app.ViewModel { return app.ViewModel{} },
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); controller.awaitRepositorySettled(ctx, `E:\nieznany`) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the context did not release the wait")
	}
}

type measuredCreator struct {
	polls    int32
	statuses []string
	progress []*platform.ProgressMeasure
}

func (c *measuredCreator) CreateRepository(context.Context, string, string, string) (string, error) {
	return "op", nil
}

func (c *measuredCreator) CreationStatus(context.Context, string) (string, string, error) {
	return "", "", errors.New("the measured path must be used")
}

func (c *measuredCreator) CreationProgress(context.Context, string) (string, string, *platform.ProgressMeasure, error) {
	i := int(atomic.AddInt32(&c.polls, 1)) - 1
	if i >= len(c.statuses) {
		i = len(c.statuses) - 1
	}
	return c.statuses[i], "", c.progress[i], nil
}

type updatableProgress struct {
	fakeProgress
	updates []platform.ProgressMeasure
}

func (f *updatableProgress) ShowUpdatableProgress(ctx context.Context, request platform.ProgressRequest) (func(platform.ProgressMeasure), func(), error) {
	close, err := f.ShowProgress(ctx, request)
	return func(measure platform.ProgressMeasure) { f.updates = append(f.updates, measure) }, close, err
}

// The creation poll carries the initial publication's measured progress to
// the import wait - the only place it can go, because the repository is not
// in the snapshot yet. A poll without a measure (before sending starts)
// changes nothing on screen.
func TestCreationPollFeedsTheMeasuredImportProgress(t *testing.T) {
	creator := &measuredCreator{
		statuses: []string{"request_pending", "request_pending", "request_pending", "attached"},
		progress: []*platform.ProgressMeasure{nil, {FilesDone: 0, FilesTotal: 330, BytesTotal: 227 << 20}, {FilesDone: 200, FilesTotal: 330, BytesSent: 150 << 20, BytesTotal: 227 << 20}, nil},
	}
	presenter := &updatableProgress{}
	controller := &Controller{cfg: Config{Progress: presenter, RepositoryCreator: creator, CreationStatusPollInterval: time.Millisecond, CreationStatusPollTimeout: 5 * time.Second}}
	update, close := controller.showUpdatableProgressKey(context.Background(), "progress.createRepository.import", nil, "T", "B")
	controller.awaitCreationOutcome(context.Background(), "demo", "Projekt testowy", "op", update)
	close()
	want := []platform.ProgressMeasure{{FilesDone: 0, FilesTotal: 330, BytesTotal: 227 << 20}, {FilesDone: 200, FilesTotal: 330, BytesSent: 150 << 20, BytesTotal: 227 << 20}}
	if len(presenter.updates) != len(want) || presenter.updates[0] != want[0] || presenter.updates[1] != want[1] {
		t.Fatalf("updates = %+v", presenter.updates)
	}
	if atomic.LoadInt32(&presenter.closed) != 1 {
		t.Fatal("the import wait was not closed")
	}
}

// A presenter without ProgressUpdater still shows the wait; the update is a
// harmless no-op.
func TestUpdatableProgressFallsBackToThePlainWait(t *testing.T) {
	plain := &fakeProgress{}
	controller := &Controller{cfg: Config{Progress: plain}}
	update, close := controller.showUpdatableProgressKey(context.Background(), "progress.createRepository.import", nil, "T", "B")
	update(platform.ProgressMeasure{FilesDone: 1, FilesTotal: 2})
	close()
	if atomic.LoadInt32(&plain.calls) != 1 || atomic.LoadInt32(&plain.closed) != 1 {
		t.Fatalf("plain presenter: calls=%d closed=%d", plain.calls, plain.closed)
	}
	none := &Controller{}
	update, close = none.showUpdatableProgressKey(context.Background(), "k", nil, "T", "B")
	update(platform.ProgressMeasure{})
	close()
}

func TestShowProgressClosesTheWindowItOpened(t *testing.T) {
	fake := &fakeProgress{}
	controller := &Controller{cfg: Config{Progress: fake}}

	close := controller.showProgress(context.Background(), "Tworzenie repozytorium", "DREWNIANA — trwa import początkowy…")
	if got := atomic.LoadInt32(&fake.calls); got != 1 {
		t.Fatalf("ShowProgress called %d times, want 1", got)
	}
	if atomic.LoadInt32(&fake.closed) != 0 {
		t.Fatal("window closed before the operation finished")
	}
	if fake.request.Title != "Tworzenie repozytorium" || fake.request.Text == "" {
		t.Fatalf("request = %+v", fake.request)
	}

	close()
	if got := atomic.LoadInt32(&fake.closed); got != 1 {
		t.Fatalf("closer ran %d times, want 1", got)
	}
}
