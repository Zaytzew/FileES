//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestAnchorProviderWatchEndsOnlyAHelperWindowsNoLongerKnows(t *testing.T) {
	connected := cfapiAnswer{OK: true, Ours: true, Status: 3221225473}
	gone := cfapiAnswer{OK: true, Ours: true, Status: 0}
	for _, test := range []struct {
		name    string
		answers []cfapiAnswer
		errs    []error
		lost    bool
	}{
		{name: "connected", answers: []cfapiAnswer{connected, connected, connected, connected}},
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
				watchAnchorProvider(ctx, time.Millisecond, func(context.Context) (cfapiAnswer, error) {
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
