package main

import (
	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"testing"
)

func TestPublicShareResendProjectionAndDispatchFence(t *testing.T) {
	for _, available := range []bool{true, false} {
		s, ok := projectPublicShares(platform.PublicShareDialogRequest{ServerID: "server", RepoID: "repo", Shares: []platform.PublicShareSummary{
			{ChannelID: "active", State: "active", Recipients: "a@example.test", CanResend: available},
			{ChannelID: "revoked", State: "revoked", Recipients: "a@example.test", CanResend: true},
			{ChannelID: "open", State: "active", CanResend: true},
		}})
		if !ok || s.Shares[0].CanResend != available || s.Shares[1].CanResend || s.Shares[2].CanResend {
			t.Fatal(s)
		}
		if shareChoiceAllowed(s.Shares, platform.PublicShareDialogResend, "active") != available || shareChoiceAllowed(s.Shares, platform.PublicShareDialogResend, "revoked") || shareChoiceAllowed(s.Shares, platform.PublicShareDialogResend, "open") || shareChoiceAllowed(s.Shares, platform.PublicShareDialogResend, "unknown") {
			t.Fatal("dispatch bypass")
		}
	}
	d, err := publicShareDeclarationToContract(actions.PublicShareDeclaration{ManageInvitations: true, ResendInvitations: true, InvitationMessage: "Hello", Recipients: []string{"a@example.test"}})
	if err != nil || !d.ManageInvitations || !d.ResendInvitations || d.InvitationMessage != "Hello" {
		t.Fatal(d, err)
	}
}
