package main

import (
	"context"
	"encoding/json"
	"errors"
	control "filees/pkg/control/v1"
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
				return control.Result{}, errors.New("unknown field include_invitations")
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
