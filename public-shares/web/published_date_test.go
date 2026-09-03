package web

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"filees/public-shares/channel"
)

// The header answers "what am I looking at", so it has to date the content and
// not the share. UpdatedAt moves when a share is defined, redefined, revoked or
// deleted and never when the repository changes, so a link following HEAD
// showed a listing current to the minute under a date weeks old - the owner
// added a file and read 14 August above it.
func TestTheHeaderDatesTheContentNotTheShare(t *testing.T) {
	defined := time.Date(2026, 8, 14, 9, 12, 0, 0, time.UTC)
	committed := time.Date(2026, 9, 3, 11, 40, 0, 0, time.UTC)

	got := publishedAt(channel.Projection{UpdatedAt: defined, RevisionAt: committed})
	if !got.Equal(committed) {
		t.Fatalf("the header must date the revision being served, got %s", got)
	}
}

// Absent, not wrong. A source that cannot date a revision - an older authority,
// or an embedding that never implements DateSource - leaves the field zero, and
// the share definition is then the best thing known rather than a lie.
func TestTheShareDateIsTheFallbackAndOnlyThat(t *testing.T) {
	defined := time.Date(2026, 8, 14, 9, 12, 0, 0, time.UTC)
	if got := publishedAt(channel.Projection{UpdatedAt: defined}); !got.Equal(defined) {
		t.Fatalf("without a revision date the share date must still show, got %s", got)
	}
}

// The rendered header, end to end: the label says what the date means and the
// action beside it reads as the continuation of that sentence.
func TestTheListingSaysPublishedAndOffersTheLatest(t *testing.T) {
	f := newWebFixture(t, nil)
	// The bare address hands out a visit and redirects; the listing is what the
	// visitor sees after following it.
	visit := visitFromRedirect(t, perform(f.handler, "GET", "https://example.test/atmprojekt/przetarg-2026", "", nil))
	listing := perform(f.handler, "GET", "https://example.test/atmprojekt/przetarg-2026?v="+url.QueryEscape(visit), "", nil)
	body := listing.Body.String()
	if !strings.Contains(body, "opublikowano ") {
		t.Fatalf("the header does not say what its date means: %s", body)
	}
	if strings.Contains(body, "stan ") {
		t.Fatalf("the old label described the state of the data, which this date is not: %s", body)
	}
	if !strings.Contains(body, "Sprawdź najnowsze wydanie") {
		t.Fatalf("the continuation of that sentence is missing: %s", body)
	}
}
