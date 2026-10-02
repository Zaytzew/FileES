//go:build !windows

package web

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"filees/public-shares/channel"
	"filees/public-shares/manifest"
)

func TestPublicPagesFollowSystemDarkPreferenceWithPinnedStyles(t *testing.T) {
	projection := channel.Projection{Alias: "example", Slug: "files"}
	for name, render := range map[string]func(*httptest.ResponseRecorder){
		"listing":   func(w *httptest.ResponseRecorder) { Handler{}.renderListing(w, projection, visit{}, "v", false) },
		"password":  func(w *httptest.ResponseRecorder) { Handler{}.renderPassword(w, projection) },
		"recipient": func(w *httptest.ResponseRecorder) { Handler{}.renderRecipient(w, projection, "", false, false) },
		"not-found": func(w *httptest.ResponseRecorder) { renderNotFound(w, notFoundTemplate) },
		"upload":    func(w *httptest.ResponseRecorder) { Handler{}.renderUpload(w, channel.UploadProjection{}, false, "") },
		"upload-otp": func(w *httptest.ResponseRecorder) {
			Handler{}.renderUploadOTP(w, channel.UploadProjection{}, false, false, "")
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			render(w)
			body := w.Body.String()
			start := strings.Index(body, "<style>")
			end := strings.Index(body, "</style>")
			if start < 0 || end < start {
				t.Fatal("page has no embedded stylesheet")
			}
			css := body[start+len("<style>") : end]
			if !strings.Contains(css, "@media(prefers-color-scheme:dark)") || !strings.Contains(css, "color-scheme:dark") {
				t.Fatal("system dark theme is missing")
			}
			digest := sha256.Sum256([]byte(css))
			hash := base64.StdEncoding.EncodeToString(digest[:])
			if !strings.Contains(w.Header().Get("Content-Security-Policy"), "style-src 'sha256-"+hash+"'") {
				t.Fatal("CSP does not authorize exactly the rendered stylesheet")
			}
		})
	}
}

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

// A HEAD-following link dates the latest revision. A pinned release retains
// the publication label; freezing a visit alone does not turn it into one.
func TestTheListingLabelsFollowingAndPinnedReleases(t *testing.T) {
	f := newWebFixture(t, nil)
	// The bare address hands out a visit and redirects; the listing is what the
	// visitor sees after following it.
	visit := visitFromRedirect(t, perform(f.handler, "GET", "https://example.test/atmprojekt/przetarg-2026", "", nil))
	listing := perform(f.handler, "GET", "https://example.test/atmprojekt/przetarg-2026?v="+url.QueryEscape(visit), "", nil)
	body := listing.Body.String()
	if !strings.Contains(body, "ostatnia wersja: ") || strings.Contains(body, "opublikowano ") {
		t.Fatalf("the following link must date its latest version: %s", body)
	}
	if strings.Contains(body, "stan ") {
		t.Fatalf("the old label described the state of the data, which this date is not: %s", body)
	}
	if !strings.Contains(body, "Sprawdź najnowsze wydanie") {
		t.Fatalf("the continuation of that sentence is missing: %s", body)
	}
	pinned := int64(5)
	frozen := newWebFixture(t, func(share *manifest.Share) { share.DoNotFollow = &pinned })
	frozenVisit := visitFromRedirect(t, perform(frozen.handler, "GET", "https://example.test/atmprojekt/przetarg-2026", "", nil))
	frozenListing := perform(frozen.handler, "GET", "https://example.test/atmprojekt/przetarg-2026?v="+url.QueryEscape(frozenVisit), "", nil)
	if !strings.Contains(frozenListing.Body.String(), "opublikowano ") || strings.Contains(frozenListing.Body.String(), "ostatnia wersja: ") {
		t.Fatalf("the pinned release must keep its publication label: %s", frozenListing.Body.String())
	}
}
