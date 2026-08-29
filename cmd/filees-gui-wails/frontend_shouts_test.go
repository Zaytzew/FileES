package main

import (
	"strings"
	"testing"
)

func TestAnnouncementFrontendContainsCompletePublishAndAckFlow(t *testing.T) {
	index := embeddedFrontendFile(t, "frontend/index.html")
	script := embeddedFrontendFile(t, "frontend/app.js")
	styles := embeddedFrontendFile(t, "frontend/app.css")

	for _, required := range []string{`id="shouts-card"`, `id="shouts"`, `Ostatnie ogłoszenia`, `id="announcement-overlay"`, `Potwierdź odczyt`} {
		if !strings.Contains(index, required) {
			t.Fatalf("frontend index does not contain %q", required)
		}
	}
	for _, required := range []string{`repoAction("publish"`, `kind: "ack_notice"`, `notice_id: notice.id`, `renderShouts(snapshot)`, `openAnnouncement(button.dataset.noticeId`, `Events.On("filees:open-announcement"`} {
		if !strings.Contains(script, required) {
			t.Fatalf("frontend script does not contain %q", required)
		}
	}
	for _, required := range []string{".shout-list", ".shout-row", "#shouts-card.has-unread", "announcement-panel-alert", ".announcement-dialog"} {
		if !strings.Contains(styles, required) {
			t.Fatalf("frontend styles do not contain %q", required)
		}
	}
	for _, forbidden := range []string{`Ostatnie /shouts/`, `Shout`, `shouting commit`} {
		if strings.Contains(index, forbidden) {
			t.Fatalf("public frontend copy leaks internal term %q", forbidden)
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
