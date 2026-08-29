package main

import (
	"strings"
	"testing"
)

func TestPublicSharesFrontendContainsAggregatePresenterAndDirectActions(t *testing.T) {
	index := embeddedFrontendFile(t, "frontend/index.html")
	script := embeddedFrontendFile(t, "frontend/app.js")
	styles := embeddedFrontendFile(t, "frontend/app.css")
	repositoryScript := embeddedFrontendFile(t, "frontend/repository.js")
	repositoryStyles := embeddedFrontendFile(t, "frontend/repository.css")

	for _, required := range []string{`id="public-shares-card"`, `id="public-shares-count"`, `Udostępnienia publiczne`} {
		if !strings.Contains(index, required) {
			t.Fatalf("frontend index does not contain %q", required)
		}
	}
	for _, required := range []string{`renderPublicShares(snapshot)`, `snapshot.public_shares_known`, `data-action="manage_public_shares"`, `data-action="revoke_public_share"`, `channel_id: publicShareRow?.dataset.channelId`, `kind: "revoke_public_shares"`, `data-share-revoke-all`, `data-share-select`, `bezterminowo · wizyta 12 h`} {
		if !strings.Contains(script, required) {
			t.Fatalf("frontend script does not contain %q", required)
		}
	}
	for _, required := range []string{".dashboard-share-list", ".dashboard-share-row", ".dashboard-share-revoke"} {
		if !strings.Contains(styles, required) {
			t.Fatalf("frontend styles do not contain %q", required)
		}
	}
	if !strings.Contains(repositoryScript, "contextChanged && sharesMode && snapshot.focus_channel_id") || !strings.Contains(repositoryStyles, ".share-row.is-focused") {
		t.Fatal("direct public share editor does not focus exactly once")
	}
}
