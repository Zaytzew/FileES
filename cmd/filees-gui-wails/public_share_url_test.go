package main

import (
	"context"
	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
	"testing"
)

// Embed unused methods so this test exercises the real list adapter only.
type publicURLClient struct{ publicShareClient }

func (publicURLClient) PublicShareList(context.Context, string, string) (*contract.PublicShareListResult, error) {
	return &contract.PublicShareListResult{Shares: []contract.PublicShareSummary{{ChannelID: "one", PublicURL: "https://download.example/realm/share"}}}, nil
}

func TestPublicShareRecipientURLProjection(t *testing.T) {
	rows, err := (publicShareAdapter{client: publicURLClient{}}).ListPublicShares(t.Context(), "server", "repo")
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %+v %v", rows, err)
	}
	snapshot, ok := projectPublicShares(platform.PublicShareDialogRequest{ServerID: "server", RepoID: "repo", Shares: []platform.PublicShareSummary{{ChannelID: rows[0].ChannelID, PublicURL: rows[0].PublicURL}}})
	if !ok || len(snapshot.Shares) != 1 || snapshot.Shares[0].PublicURL != "https://download.example/realm/share" {
		t.Fatalf("URL lost: %+v", snapshot)
	}
}
