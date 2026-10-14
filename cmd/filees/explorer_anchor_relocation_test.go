//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAnchorRelocationDrainWaitsAndHonoursFence(t *testing.T) {
	for _, mode := range []string{"drain", "cancel", "changed-fence", "wrong-root", "no-fence"} {
		t.Run(mode, func(t *testing.T) {
			m, attached := anchorReconcileFixture(t)
			record, err := m.lifecycle.BeginRelocation(attached.ServerID, attached.RepoID, attached.LocalPath+"-new", true)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			providerCtx, stop := context.WithCancel(t.Context())
			done := make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-done:
				default:
					close(done)
				}
			})
			root := record.LocalPath
			if mode == "wrong-root" {
				root += "-different"
			}
			m.running = map[string]anchorConnection{anchorKey(record): {root: root, ctx: providerCtx, cancel: stop, done: done}}
			if mode == "no-fence" {
				if _, err := m.lifecycle.FailRelocation(record.OperationID, errors.New("withdrawn")); err != nil {
					t.Fatal(err)
				}
			}
			result := make(chan error, 1)
			go func() { result <- m.quiesceRelocation(ctx, record) }()
			if mode == "wrong-root" || mode == "no-fence" {
				select {
				case err := <-result:
					if err == nil || providerCtx.Err() != nil {
						t.Fatal("invalid fence stopped provider", err)
					}
				case <-time.After(time.Second):
					t.Fatal("refusal waited")
				}
				return
			}
			select {
			case <-providerCtx.Done():
			case <-time.After(time.Second):
				t.Fatal("provider not canceled")
			}
			select {
			case err := <-result:
				t.Fatal("returned before drain", err)
			default:
			}
			// Even a concurrent manager tick must not reconnect under the fence.
			m.reconcile(t.Context())
			if m.running[anchorKey(record)].ctx != providerCtx {
				t.Fatal("provider restarted while relocating")
			}
			if mode == "cancel" {
				cancel()
			} else {
				if mode == "changed-fence" {
					if _, err := m.lifecycle.FailRelocation(record.OperationID, errors.New("withdrawn during drain")); err != nil {
						t.Fatal(err)
					}
				}
				close(done)
			}
			select {
			case err := <-result:
				if mode == "cancel" && !errors.Is(err, context.Canceled) || mode == "drain" && err != nil || mode == "changed-fence" && err == nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("drain did not finish")
			}
		})
	}
}

func TestAnchorLifecycleNotificationIsBounded(t *testing.T) {
	m := &anchorManager{wake: make(chan struct{}, 1)}
	for i := 0; i < 100; i++ {
		m.notifyLifecycle()
	}
	if len(m.wake) != 1 {
		t.Fatal("notification not deduplicated")
	}
}
