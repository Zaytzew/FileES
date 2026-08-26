package main

import (
	"strings"
	"testing"
)

func TestShoutFrontendContainsCompletePublishAndAckFlow(t *testing.T) {
	index := embeddedFrontendFile(t, "frontend/index.html")
	script := embeddedFrontendFile(t, "frontend/app.js")
	styles := embeddedFrontendFile(t, "frontend/app.css")

	for _, required := range []string{`id="shouts-card"`, `id="shouts"`, `Ostatnie /shouts/`} {
		if !strings.Contains(index, required) {
			t.Fatalf("frontend index does not contain %q", required)
		}
	}
	for _, required := range []string{`data-action="publish"`, `data-action="ack_notice"`, `notice_id:`, `renderShouts(snapshot)`, `$("#shouts").addEventListener`} {
		if !strings.Contains(script, required) {
			t.Fatalf("frontend script does not contain %q", required)
		}
	}
	for _, required := range []string{".shout-list", ".shout-row", ".shout-action"} {
		if !strings.Contains(styles, required) {
			t.Fatalf("frontend styles do not contain %q", required)
		}
	}
}

func embeddedFrontendFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := frontend.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
