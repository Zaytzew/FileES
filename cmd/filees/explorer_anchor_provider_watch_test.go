//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestAnchorProviderDisconnectedOnlyKnownStates(t *testing.T) {
	for _, status := range []int{0, 0xc0000001, 1, 2, 4, 8, 0x10, 0x20, 0x40, 3, 0x80000000, 0xc0000002, -1, 0xffffffff, 0xc0000000} {
		want := status == 0 || status == 0xc0000001
		if got := anchorProviderDisconnected(status); got != want {
			t.Fatalf("status %#x: disconnected=%v, want %v", status, got, want)
		}
	}
}

func TestAnchorProviderWatchEndsOnlyAHelperWindowsNoLongerKnows(t *testing.T) {
	connected := cfapiAnswer{OK: true, Ours: true, Status: 1} // CF_PROVIDER_STATUS_IDLE
	gone := cfapiAnswer{OK: true, Ours: true, Status: 0}
	terminated := cfapiAnswer{OK: true, Ours: true, Status: 0xc0000001}
	for _, test := range []struct {
		name    string
		answers []cfapiAnswer
		errs    []error
		lost    bool
	}{
		{name: "connected", answers: []cfapiAnswer{connected, connected, connected, connected}},
		{name: "terminated is disconnected", answers: []cfapiAnswer{terminated, terminated}, lost: true},
		{name: "mixed disconnected readings", answers: []cfapiAnswer{gone, terminated}, lost: true},
		{name: "error does not prove disconnection", answers: []cfapiAnswer{{OK: true, Ours: true, Status: 0xc0000002}, {OK: true, Ours: true, Status: 0xc0000002}}},
		{name: "filter detached and attached again", answers: []cfapiAnswer{connected, gone, gone}, lost: true},
		{name: "one zero reading is not enough", answers: []cfapiAnswer{gone, connected, gone, connected}},
		{name: "unreadable answers prove nothing", answers: []cfapiAnswer{gone, {}, gone, {}}, errs: []error{nil, errors.New("helper"), nil, nil}},
		{name: "another provider's root is not ours to judge", answers: []cfapiAnswer{{OK: true, Status: 0}, {OK: true, Status: 0}, {OK: true, Status: 0}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls, lost atomic.Int32
			done := make(chan struct{})
			go func() {
				defer close(done)
				watchAnchorProvider(ctx, time.Millisecond, nil, func(context.Context) (cfapiAnswer, error) {
					index := int(calls.Add(1)) - 1
					if index >= len(test.answers) {
						cancel()
						return connected, nil
					}
					var err error
					if index < len(test.errs) {
						err = test.errs[index]
					}
					return test.answers[index], err
				}, func() { lost.Add(1) })
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("watch did not end")
			}
			if got := lost.Load() == 1; got != test.lost {
				t.Fatalf("lost=%d, want lost %v after %d readings", lost.Load(), test.lost, calls.Load())
			}
		})
	}
}

func TestAnchorProviderWatchActsAtOnceWhenTheFilterIsKnownToHaveBeenDetached(t *testing.T) {
	m := &anchorManager{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls, lost atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The routine check is an hour away: only the prompt can cause a look.
		watchAnchorProvider(ctx, time.Hour, m.providerSuspected, func(context.Context) (cfapiAnswer, error) {
			calls.Add(1)
			return cfapiAnswer{OK: true, Ours: true, Status: 0}, nil
		}, func() { lost.Add(1) })
	}()
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("checked without a reason")
	}
	m.suspectProviders()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not react to the prompt")
	}
	if calls.Load() != 1 || lost.Load() != 1 {
		t.Fatalf("calls=%d lost=%d, want one look and one reconnect", calls.Load(), lost.Load())
	}
}
