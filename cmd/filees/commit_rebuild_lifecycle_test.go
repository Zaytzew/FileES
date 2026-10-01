package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/config"
)

func TestCanonicalRecoveryLifecycleRequiresBothAcknowledgements(t *testing.T) {
	for _, mode := range []string{"success", "recovery-failure", "resume-failure", "cancel-after-dispatch", "quiesce-refused"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			events := make(chan provisionedAttachment)
			entered := make(chan struct{}, 1)
			failure := errors.New("injected failure")
			fn := canonicalRecoveryLifecycle(t.Context(), events, func(callCtx context.Context, _ config.Repo, _ client.Client) error {
				entered <- struct{}{}
				if mode == "recovery-failure" {
					return failure
				}
				return callCtx.Err()
			})
			result := make(chan error, 1)
			go func() { result <- fn(ctx, config.Repo{ID: "repo"}, nil) }()
			var stop provisionedAttachment
			select {
			case stop = <-events:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if !stop.Quiesce || stop.Repo.ID != "repo" {
				t.Fatal(stop)
			}
			select {
			case <-entered:
				t.Fatal("reconstruction ran before stop acknowledgement")
			default:
			}
			if mode == "quiesce-refused" {
				stop.Result <- failure
				if err := <-result; !errors.Is(err, failure) {
					t.Fatal(err)
				}
				select {
				case <-entered:
					t.Fatal("reconstruction ran after refusal")
				default:
				}
				return
			}
			if mode == "cancel-after-dispatch" {
				cancel()
			}
			stop.Result <- nil
			var start provisionedAttachment
			select {
			case start = <-events:
			case <-time.After(5 * time.Second):
				t.Fatal("resume not dispatched")
			}
			if start.Quiesce || start.Repo.ID != "repo" {
				t.Fatal(start)
			}
			select {
			case <-entered:
			default:
				t.Fatal("resume preceded reconstruction")
			}
			select {
			case <-result:
				t.Fatal("returned before restart acknowledgement")
			default:
			}
			if mode == "resume-failure" {
				start.Result <- failure
			} else {
				start.Result <- nil
			}
			err := <-result
			switch mode {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "cancel-after-dispatch":
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			default:
				if !errors.Is(err, failure) {
					t.Fatal(err)
				}
			}
		})
	}
}
