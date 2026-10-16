package main

import (
	"context"
	"encoding/json"
	"errors"
	control "filees/pkg/control/v1"
	"fmt"
	"golang.org/x/crypto/ssh"
	"testing"
)

func TestPublicShareInvitationListingFallback(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		calls := 0
		listed, err := listPublicSharesWithOptions(t.Context(), "repo", func(p control.ListPublicSharesPayload) (control.Result, error) {
			calls++
			if p.RepoID != "repo" {
				t.Fatal("wrong repo")
			}
			if legacy && p.IncludeInvitations {
				return control.Result{}, oldInvitationParserError()
			}
			raw, _ := json.Marshal(control.ListPublicSharesResult{CanManageInvitations: !legacy})
			return control.Result{Result: raw}, nil
		})
		want := 1
		if legacy {
			want = 2
		}
		if err != nil || calls != want || listed.CanManageInvitations == legacy {
			t.Fatal(calls, listed, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	_, err := listPublicSharesWithOptions(ctx, "repo", func(control.ListPublicSharesPayload) (control.Result, error) {
		calls++
		cancel()
		return control.Result{}, context.Canceled
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("canceled read retried")
	}
}

func oldInvitationParserError() error {
	return fmt.Errorf("repository control failed: %w: ticket parse: json: unknown field \"include_invitations\"", &ssh.ExitError{})
}

func TestInvitationListingDoesNotRetryOperationalFailures(t *testing.T) {
	for _, cause := range []error{
		context.DeadlineExceeded,
		context.Canceled,
		errors.New("dial tcp: connection refused"),
		errors.New("server rejected identity request: ACCESS_DENIED"),
		errors.New(`json: unknown field "include_invitations"`),
		fmt.Errorf("repository control failed: %w: identity refused", &ssh.ExitError{}),
		fmt.Errorf("repository control failed: %w: json: unknown field \"other\"", &ssh.ExitError{}),
		errors.Join(oldInvitationParserError(), context.DeadlineExceeded),
	} {
		for _, shelf := range []bool{false, true} {
			calls := 0
			var err error
			if shelf {
				_, err = listUploadChannelsWithOptions(t.Context(), "repo", func(control.ListUploadChannelsPayload) (control.Result, error) {
					calls++
					return control.Result{}, cause
				})
			} else {
				_, err = listPublicSharesWithOptions(t.Context(), "repo", func(control.ListPublicSharesPayload) (control.Result, error) { calls++; return control.Result{}, cause })
			}
			if calls != 1 || !errors.Is(err, cause) {
				t.Fatalf("shelf=%v calls=%d cause=%v err=%v", shelf, calls, cause, err)
			}
		}
	}
}
