package web

import (
	"crypto/rand"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"filees/public-shares/authority"
	"filees/public-shares/gate"
	"filees/public-shares/manifest"

	"github.com/google/uuid"
)

// The invariant a public link must keep: if the listing renders for a visit,
// what the listing shows must be reachable with that same visit.
//
// It used to break on the mildest edit possible. The frost proof carried by a
// visit covers a digest of the entire channel record, so renaming one file -
// granting nothing, revoking nothing, removing nothing - invalidated every
// outstanding visit on the object path while leaving the listing perfectly
// renderable. The visitor got a correct listing where every file returned 404,
// which is worse than a refusal because it says the content is there and then
// denies each item without a reason.
//
// A link that follows HEAD answers a stale snapshot with a new snapshot. The
// subject is what carries authorization and it is re-checked against the live
// record on every request, so refreshing grants nothing the holder did not
// already have.
func TestLiveLinkSurvivesAManifestEditMidVisit(t *testing.T) {
	f := newWebFixture(t, nil)
	visit := visitFromRedirect(t, perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026", "", nil))

	edited := f.share
	edited.Objects = []manifest.Object{{
		PublicID:    "7f3a1c9e2b4d6a80",
		RepoPath:    "wydanie/projekt.pdf",
		DisplayName: "Projekt budowlany (po korekcie).pdf",
	}}
	if _, _, err := f.store.Update(uuid.NewString(), f.owner, f.channelID, edited); err != nil {
		t.Fatalf("rename one file: %v", err)
	}

	listing := perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026?v="+url.QueryEscape(visit), "", nil)
	if listing.Code != http.StatusOK {
		t.Fatalf("listing after a rename status=%d", listing.Code)
	}
	// The path a click actually takes: prepare, then follow the redirect it
	// issues. Using the visit the visitor still holds, not a fresh one.
	prepared := perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026/get/7f3a1c9e2b4d6a80?v="+url.QueryEscape(visit), "", nil)
	if prepared.Code != http.StatusSeeOther {
		t.Fatalf("the listing rendered, so clicking its own file must not be refused; got status=%d body=%s", prepared.Code, prepared.Body.String())
	}
	file := perform(f.handler, http.MethodGet, "https://example.test"+prepared.Header().Get("Location"), "", nil)
	if file.Code != http.StatusOK {
		t.Fatalf("following the prepared redirect must deliver the file; got status=%d", file.Code)
	}
	if file.Body.String() != "revision five payload" {
		t.Fatalf("file body = %q", file.Body.String())
	}
}

// Withdrawing a file is a different event from a stale snapshot, and the
// visitor is owed the difference. A bare 404 leaves them hunting for something
// that is no longer published; this says what happened and hands them the
// current listing instead of the one they clicked from.
func TestWithdrawnFileExplainsItselfAndShowsTheCurrentListing(t *testing.T) {
	f := newWebFixture(t, func(share *manifest.Share) {
		share.Objects = append(share.Objects, manifest.Object{
			PublicID:    "1122334455667788",
			RepoPath:    "wydanie/aneks.pdf",
			DisplayName: "Aneks.pdf",
		})
	})
	visit := visitFromRedirect(t, perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026", "", nil))

	withdrawn := f.share
	withdrawn.Objects = []manifest.Object{{
		PublicID:    "7f3a1c9e2b4d6a80",
		RepoPath:    "wydanie/projekt.pdf",
		DisplayName: "Projekt budowlany.pdf",
	}}
	if _, _, err := f.store.Update(uuid.NewString(), f.owner, f.channelID, withdrawn); err != nil {
		t.Fatalf("withdraw one file: %v", err)
	}

	response := perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026/file/1122334455667788?v="+url.QueryEscape(visit), "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("a withdrawn file should explain itself rather than 404 into silence; status=%d", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, "nie jest już częścią tego udostępnienia") {
		t.Fatalf("the visitor is not told what happened: %s", body)
	}
	if !strings.Contains(body, "Projekt budowlany.pdf") {
		t.Fatalf("the current listing should come with the explanation: %s", body)
	}
	if strings.Contains(body, "Aneks.pdf") {
		t.Fatalf("the withdrawn file must not still be offered: %s", body)
	}
}

// The other half of the contract. A release link is pinned to one revision and
// must never advance: that stability is the whole reason to publish one, and a
// refresh would quietly hand the recipient different bytes than the sender
// meant. Without this, the change that makes live links follow HEAD would look
// identical whether or not it respects pinning.
func TestPinnedShareDoesNotFollowHead(t *testing.T) {
	pinned := int64(5)
	f := newWebFixture(t, func(share *manifest.Share) { share.DoNotFollow = &pinned })
	visit := visitFromRedirect(t, perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026", "", nil))

	f.source.head = 6
	f.source.values[6] = "new payload"
	newSize := int64(len("new payload"))
	f.source.trees[6] = []authority.TreeObject{{RepoPath: "wydanie/nowy.txt", DisplayName: "nowy.txt", Size: &newSize}}

	listing := perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026?v="+url.QueryEscape(visit), "", nil)
	if listing.Code != http.StatusOK {
		t.Fatalf("pinned listing status=%d", listing.Code)
	}
	if strings.Contains(listing.Body.String(), "nowy.txt") {
		t.Fatalf("a pinned release must not advance to a newer revision: %s", listing.Body.String())
	}
	if !strings.Contains(listing.Body.String(), "Projekt budowlany.pdf") {
		t.Fatalf("a pinned release must keep serving its own revision: %s", listing.Body.String())
	}
}

// An expired visit on an open link is not a refusal. The visitor is holding the
// URL the redirect handed them, and their entitlement has not changed - only
// the clock moved. Before this, that URL died after the lifetime while the same
// link without the parameter kept working, which is a dead end nobody can
// diagnose from the outside.
func TestExpiredVisitOnAnOpenLinkStartsAFreshOne(t *testing.T) {
	f := newWebFixture(t, nil)
	visit := visitFromRedirect(t, perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026", "", nil))

	*f.now = f.now.Add(visitLifetime + time.Hour)

	response := perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026?v="+url.QueryEscape(visit), "", nil)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("an expired visit on an open link should start a fresh one, not refuse; status=%d", response.Code)
	}
	fresh := visitFromRedirect(t, response)
	if fresh == visit {
		t.Fatal("the fresh visit must not be the expired one")
	}
	listing := perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026?v="+url.QueryEscape(fresh), "", nil)
	if listing.Code != http.StatusOK || !strings.Contains(listing.Body.String(), "Projekt budowlany.pdf") {
		t.Fatalf("the fresh visit must render the listing: status=%d", listing.Code)
	}
}

// The same expiry on a gated link must meet the gate again. Falling through is
// what makes that automatic: nothing is known about this visitor any more, so
// the channel gets to ask. A password link that let an expired capability back
// in would be worse than one that refuses.
func TestExpiredVisitOnAPasswordLinkAsksAgain(t *testing.T) {
	f := newWebFixture(t, nil)
	hash, err := gate.HashPassword("otwiera-sezam", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	guarded := f.share
	guarded.Password = hash
	if _, _, err := f.store.Update(uuid.NewString(), f.owner, f.channelID, guarded); err != nil {
		t.Fatalf("put a password on the channel: %v", err)
	}

	response := perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "password") {
		t.Fatalf("a password link must ask before it shows anything: status=%d", response.Code)
	}
}
