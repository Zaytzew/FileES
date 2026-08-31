package main

import (
	"strings"
	"testing"
)

func TestLockReleaseFrontendContainsRequestAndHolderActions(t *testing.T) {
	script := embeddedFrontendFile(t, "frontend/app.js")
	styles := embeddedFrontendFile(t, "frontend/app.css")
	for _, required := range []string{
		`snapshot.lock_release_requests`, `data-lock-release-request-id`,
		`data-action="request_lock_release"`, `data-action="dismiss_lock_release"`,
		`data-action="accept_lock_release"`, `lock_release_request_id:`,
		"Poproś o zwolnienie", "prośba wysłana",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("frontend script does not contain %q", required)
		}
	}
	for _, required := range []string{".lock-release-request", ".reservation-request-actions", ".lock-owner.waiting"} {
		if !strings.Contains(styles, required) {
			t.Fatalf("frontend styles do not contain %q", required)
		}
	}
}
