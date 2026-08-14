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
