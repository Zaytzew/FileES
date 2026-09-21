package main

import (
	control "filees/pkg/control/v1"
	"testing"
)

func TestPublicShareRecipientURLFromControl(t *testing.T) {
	const want = "https://download.example/realm/share"
	got := publicShareSummaryFromControl(control.PublicShareSummary{PublicURL: want})
	if got.PublicURL != want {
		t.Fatalf("URL lost: %+v", got)
	}
}
