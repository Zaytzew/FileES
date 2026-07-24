package main

import (
	"context"
	"testing"

	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/pkg/localpin"
)

func newTestPinStore(t *testing.T) *localpin.Store {
	t.Helper()
	store, err := localpin.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRequireLocalPinAtLaunchNoOpWhenNotRequired(t *testing.T) {
	fake := &platformtest.Fake{PromptTextFunc: func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
		t.Fatal("prompted despite RequireOnLaunch being false")
		return platform.PromptTextResult{}, nil
	}}
	if err := requireLocalPinAtLaunch(context.Background(), fake, nil); err != nil {
		t.Fatalf("nil store: %v", err)
	}
	store := newTestPinStore(t)
	if err := requireLocalPinAtLaunch(context.Background(), fake, store); err != nil {
		t.Fatalf("unconfigured store: %v", err)
	}
	if err := store.Setup([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if err := requireLocalPinAtLaunch(context.Background(), fake, store); err != nil {
		t.Fatalf("configured but RequireOnLaunch=false: %v", err)
	}
}

func TestRequireLocalPinAtLaunchAcceptsCorrectPIN(t *testing.T) {
	store := newTestPinStore(t)
	if err := store.Setup([]byte("4242")); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRequireOnLaunch(true); err != nil {
		t.Fatal(err)
	}
	fake := &platformtest.Fake{PromptTextFunc: func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
		return platform.PromptTextResult{Value: "4242"}, nil
	}}
	if err := requireLocalPinAtLaunch(context.Background(), fake, store); err != nil {
		t.Fatalf("correct PIN rejected: %v", err)
	}
}

func TestRequireLocalPinAtLaunchRefusesAfterAttemptsExhausted(t *testing.T) {
	store := newTestPinStore(t)
	if err := store.Setup([]byte("4242")); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRequireOnLaunch(true); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fake := &platformtest.Fake{PromptTextFunc: func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
		calls++
		return platform.PromptTextResult{Value: "wrong"}, nil
	}}
	if err := requireLocalPinAtLaunch(context.Background(), fake, store); err == nil {
		t.Fatal("wrong PIN repeatedly accepted")
	}
	if calls != maxLaunchPinAttempts {
		t.Fatalf("prompt calls=%d, want %d", calls, maxLaunchPinAttempts)
	}
}

func TestRequireLocalPinAtLaunchRefusesOnCancel(t *testing.T) {
	store := newTestPinStore(t)
	if err := store.Setup([]byte("4242")); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRequireOnLaunch(true); err != nil {
		t.Fatal(err)
	}
	fake := &platformtest.Fake{PromptTextFunc: func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
		return platform.PromptTextResult{Cancelled: true}, nil
	}}
	if err := requireLocalPinAtLaunch(context.Background(), fake, store); err == nil {
		t.Fatal("cancelled prompt did not refuse launch")
	}
}
