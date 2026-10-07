package main

import (
	"context"
	"encoding/json"
	"errors"
	control "filees/pkg/control/v1"
	"testing"
)

func TestShelfInvitationListingLegacyFallbackIsReadOnly(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		calls := 0
		listed, err := listUploadChannelsWithOptions(t.Context(), "repo", func(p control.ListUploadChannelsPayload) (control.Result, error) {
			calls++
			if p.AuthorityRepoID != "repo" {
				t.Fatal("wrong repository")
			}
			if legacy && p.IncludeInvitations {
				return control.Result{}, errors.New("unknown field include_invitations")
			}
			raw, _ := json.Marshal(control.ListUploadChannelsResult{CanManageInvitations: !legacy})
			return control.Result{Result: raw}, nil
		})
		want := 1
		if legacy {
			want = 2
		}
		if err != nil || calls != want || listed.CanManageInvitations == legacy {
			t.Fatalf("legacy=%v calls=%d listed=%+v err=%v", legacy, calls, listed, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	_, err := listUploadChannelsWithOptions(ctx, "repo", func(control.ListUploadChannelsPayload) (control.Result, error) {
		calls++
		cancel()
		return control.Result{}, context.Canceled
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("canceled listing retried")
	}
}
