package commit

import (
	"bytes"
	"filees/pkg/errmap"
	"filees/pkg/talk"
	"strings"
	"testing"
)

// Owner's production, 2026-09-25: working copies inside the Nextcloud folder
// (virtual files) lost every new file from publication, because Nextcloud
// turns each saved file into its own placeholder within seconds and the add
// path took any provider's placeholder for an anchor's.
func TestPlaceholderAddDecision(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		anchored, placeholder, notOnDisk bool
		want                             placeholderAddAction
	}{
		{"ordinary file", false, false, false, placeholderAddIt},
		{"Nextcloud placeholder with its bytes on disk", false, true, false, placeholderAddIt},
		{"Nextcloud placeholder freed from this disk", false, true, true, placeholderWait},
		{"anchor placeholder not opened here", true, true, true, placeholderDrop},
		{"anchor placeholder already fetched", true, true, false, placeholderDrop},
		{"anchored copy, file saved by the person", true, false, false, placeholderAddIt},
	} {
		if got := placeholderAdd(tc.anchored, tc.placeholder, tc.notOnDisk); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestForeignPlaceholderWaitIsVisibleOnceAndKeepsPendingWork(t *testing.T) {
	item := &stageItem{Rel: "report.dwg", ver: 7}
	var journal bytes.Buffer
	s := &Service{staging: map[string]*stageItem{item.Rel: item}, ErrSink: errmap.NewSink(&journal, "fixture"), Logger: talk.With("placeholder-wait")}
	pending := []pendingEntry{{item: item, ver: 7}}
	for range 100 {
		s.reportForeignPlaceholderWait(item.Rel, pending)
	}
	if bytes.Count(journal.Bytes(), []byte("\n")) != 1 || !strings.Contains(journal.String(), "commit.foreign_placeholder_wait") || !strings.Contains(journal.String(), "report.dwg") || s.staging[item.Rel] != item || item.ver != 7 {
		t.Fatalf("lost work or repeated diagnostic: %s", journal.String())
	}
	s.markPlaceholderWait(item.Rel, pending, false) // Bytes became available.
	s.reportForeignPlaceholderWait(item.Rel, pending)
	if bytes.Count(journal.Bytes(), []byte("\n")) != 2 {
		t.Fatal(journal.String())
	}
	item.ver++
	s.markPlaceholderWait(item.Rel, pending, false) // Stale batch must not clear newer state.
	if !item.placeholderWaitReported {
		t.Fatal("stale batch cleared notification")
	}
}
