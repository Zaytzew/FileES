package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	contract "filees/pkg/contract/v1"
	control "filees/pkg/control/v1"
	"golang.org/x/crypto/ssh"
)

// Management-only listing: invitation messages do not enter the dashboard cache.
func (s *realmAliasService) ListPublicSharesWithInvitations(ctx context.Context, serverID, repoID string) (contract.PublicShareListResult, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return contract.PublicShareListResult{}, fmt.Errorf("no activated profile for server %q", serverID)
	}
	listed, err := listPublicSharesWithOptions(ctx, repoID, func(p control.ListPublicSharesPayload) (control.Result, error) {
		return s.exchange(ctx, profile, control.TicketListPublicShares, p)
	})
	if err != nil {
		return contract.PublicShareListResult{}, err
	}
	result := contract.PublicShareListResult{CanManageInvitations: listed.CanManageInvitations, Shares: make([]contract.PublicShareSummary, 0, len(listed.Shares))}
	for _, share := range listed.Shares {
		result.Shares = append(result.Shares, publicShareSummaryFromControl(share))
	}
	return result, nil
}

func listPublicSharesWithOptions(ctx context.Context, repoID string, exchange func(control.ListPublicSharesPayload) (control.Result, error)) (control.ListPublicSharesResult, error) {
	result, err := exchange(control.ListPublicSharesPayload{RepoID: repoID, IncludeInvitations: true})
	if ctx.Err() == nil && invitationListingUnsupported(err) {
		// Only explicit old-parser refusal permits another read. Retrying a
		// timeout/ACL/transport failure doubles latency and hides its cause.
		result, err = exchange(control.ListPublicSharesPayload{RepoID: repoID})
	}
	if err != nil {
		return control.ListPublicSharesResult{}, err
	}
	var listed control.ListPublicSharesResult
	if err := control.DecodeResultPayload(result.Result, &listed); err != nil {
		return listed, err
	}
	return listed, nil
}

// The pinned control transport wraps the remote parser's nonzero exit. Plain
// strings from local validation, lost replies and independent cancellation are
// not capability negotiation. This helper never applies to mutations.
func invitationListingUnsupported(err error) bool {
	var exit *ssh.ExitError
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) &&
		errors.As(err, &exit) && strings.Contains(err.Error(), `json: unknown field "include_invitations"`)
}

func (s refreshingPublicShareService) ListPublicSharesWithInvitations(ctx context.Context, serverID, repoID string) (contract.PublicShareListResult, error) {
	if capable, ok := s.delegate.(interface {
		ListPublicSharesWithInvitations(context.Context, string, string) (contract.PublicShareListResult, error)
	}); ok {
		return capable.ListPublicSharesWithInvitations(ctx, serverID, repoID)
	}
	shares, err := s.delegate.ListPublicShares(ctx, serverID, repoID)
	return contract.PublicShareListResult{Shares: shares}, err
}
