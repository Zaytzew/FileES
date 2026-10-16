package passport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type heartbeatCountingBackend struct {
	*fakeBackend
	inspections int
}

func (b *heartbeatCountingBackend) Inspect(ctx context.Context, path string) (*Lock, error) {
	b.inspections++
	return b.fakeBackend.Inspect(ctx, path)
}

func TestHeartbeatReusesOneEmissionWithoutWeakeningMutations(t *testing.T) {
	for _, mode := range []string{"unchanged", "revoked", "failed", "expired", "future", "no-time", "predates-acquire", "renew", "close", "canceled", "ages-during-read"} {
		t.Run(mode, func(t *testing.T) {
			wc := t.TempDir()
			if err := os.Mkdir(filepath.Join(wc, ".svn"), 0700); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			backend := &heartbeatCountingBackend{fakeBackend: newFakeBackend()}
			view := OwnershipView{Holds: map[string]OwnershipHold{}}
			reads := 0
			manager, err := Open(filepath.Join(wc, "passports.json"), "instance", backend, Config{
				WorkingCopy: wc, Now: func() time.Time { return now },
				HeartbeatOwnership: func(context.Context) (OwnershipView, error) {
					reads++
					if mode == "failed" {
						return OwnershipView{}, errPartitioned
					}
					if mode == "ages-during-read" {
						now = now.Add(time.Minute)
					}
					return view, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			paths := []string{}
			for i := 0; i < 100; i++ {
				paths = append(paths, filepath.Join(wc, fmt.Sprintf("file-%03d", i)))
			}
			if _, _, err := manager.Acquire(t.Context(), paths, ""); err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				lock := backend.locks[path]
				lock.Owner = "fixture-client"
				view.Holds[filepath.Base(path)] = OwnershipHold{Token: lock.Token, Owner: lock.Owner, Comment: lock.Comment}
			}
			now = now.Add(2 * time.Minute)
			view.ObservedAt = now.Add(-time.Second)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "revoked":
				delete(view.Holds, filepath.Base(paths[0]))
			case "expired":
				view.ObservedAt = now.Add(-time.Minute)
			case "future":
				view.ObservedAt = now.Add(time.Second)
			case "no-time":
				view.ObservedAt = time.Time{}
			case "predates-acquire":
				view.ObservedAt = now.Add(-time.Second)
				for path, p := range manager.passports {
					p.LastHeartbeatAt = now
					manager.passports[path] = p
				}
				view.Holds = nil // must not falsely mark newly renewed passports lost
			case "renew":
				now = now.Add(9 * time.Minute)
			case "close":
				for path, p := range manager.passports {
					p.CloseAfter = now
					manager.passports[path] = p
				}
			case "canceled":
				cancel()
			}
			backend.inspections = 0
			err = manager.Heartbeat(ctx)
			wantError := mode == "revoked" || mode == "failed" || mode == "expired" || mode == "future" || mode == "no-time" || mode == "canceled" || mode == "ages-during-read"
			if (err != nil) != wantError {
				t.Fatalf("heartbeat error = %v", err)
			}
			if mode == "revoked" && !errors.Is(err, ErrPassportLost) {
				t.Fatalf("lost = %v", err)
			}
			wantDirect, wantReads := 0, 1
			if mode == "predates-acquire" || mode == "renew" || mode == "close" {
				wantDirect = 100
			}
			if mode == "renew" || mode == "close" || mode == "canceled" {
				wantReads = 0
			}
			if backend.inspections != wantDirect || reads != wantReads {
				t.Fatalf("direct=%d emissions=%d; want %d/%d", backend.inspections, reads, wantDirect, wantReads)
			}
			for path, p := range manager.passports {
				if mode == "revoked" && path == paths[0] {
					if p.State != StateLost {
						t.Fatal("revoked passport survived")
					}
					continue
				}
				if p.State != StateActive {
					t.Fatalf("false state change: %s", p.State)
				}
			}
		})
	}
}
