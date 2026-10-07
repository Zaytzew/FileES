package main

import (
	"filees/internal/gui/platform"
	"testing"
)

func TestShelfResendProjectionRequiresServerSupport(t *testing.T) {
	for _, available := range []bool{false, true} {
		snapshot, ok := projectUploadChannels(platform.UploadChannelDialogRequest{CanManageInvitations: available, Channels: []platform.UploadChannelSummary{{ChannelID: "active", State: "active"}, {ChannelID: "revoked", State: "revoked"}}}, RepositoryContextProjection{ServerID: "server", RepoID: "repo"})
		if !ok || snapshot.Uploads[0].CanResend != available || snapshot.Uploads[1].CanResend {
			t.Fatalf("projection: %+v", snapshot)
		}
		if uploadChoiceAllowed(snapshot.Uploads, platform.UploadChannelDialogResend, "active") != available || uploadChoiceAllowed(snapshot.Uploads, platform.UploadChannelDialogResend, "revoked") {
			t.Fatal("resend action bypassed projection")
		}
	}
}
