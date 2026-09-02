package authority

import (
	"context"
	"strings"
	"testing"

	"filees/public-shares/manifest"

	"github.com/google/uuid"
)

// A visit that can still render a listing must still be able to fetch what the
// listing shows. This measures whether that holds when the channel record
// changes underneath an outstanding visit.
//
// The two paths validate with different strength. The listing path checks the
// visit signature and that a frost proof is present at all; the object path
// recomputes the proof and compares it. The proof covers a fingerprint of the
// whole manifest and recipient list, so any edit to the record - including one
// that grants and revokes nothing - invalidates every proof already issued,
// while leaving the listing perfectly renderable.
//
// The symptom that prompted this is a public link showing a correct listing
// where every file returns 404, which is worse than an outright refusal: the
// visitor is told the content is there and then denied each item without a
// reason.
func TestListingAndObjectDisagreeAfterAManifestEdit(t *testing.T) {
	resolver, store, share, channelID := resolverFixture(t, nil)
	ctx := context.Background()

	entry, err := resolver.Enter(ctx, "atmprojekt", "przetarg-2026")
	if err != nil {
		t.Fatal(err)
	}
	request := ObjectRequest{
		ChannelID:  entry.Projection.ChannelID,
		PublicID:   share.Objects[0].PublicID,
		Revision:   entry.Revision,
		FrostProof: entry.FrostProof,
	}
	if _, err := resolver.Check(ctx, request); err != nil {
		t.Fatalf("the visit must be usable when it is issued: %v", err)
	}

	// An edit that changes no permission and removes no object: the same file
	// stays published to the same audience under a different label.
	edited := share
	edited.Objects = []manifest.Object{{
		PublicID:    share.Objects[0].PublicID,
		RepoPath:    share.Objects[0].RepoPath,
		DisplayName: "Projekt (poprawiony).pdf",
	}}
	if _, _, err := store.Update(uuid.NewString(), share.OwnerRealm, channelID, edited); err != nil {
		t.Fatalf("update the channel: %v", err)
	}

	// The listing path: what a visitor returning with the same cookie renders.
	if _, err := resolver.InspectAt(ctx, "atmprojekt", "przetarg-2026", entry.Revision); err != nil {
		t.Fatalf("listing for the outstanding visit must still render, or the asymmetry does not exist: %v", err)
	}

	// The object path: the very file that listing just showed.
	_, err = resolver.Check(ctx, request)
	if err == nil {
		return // The invariant holds; nothing to report.
	}

	t.Fatalf("listing renders for this visit but its own object is refused with %q; "+
		"a rename granted and revoked nothing, yet the frost proof covers a fingerprint of the "+
		"whole manifest, so every issued visit is invalidated by any edit at all. "+
		"The visitor sees a correct listing where every file is dead.", err)
}

// The proof is what couples the two paths, so this states plainly what it is
// derived from. It is not a policy check: it cannot distinguish an edit that
// changes authorization from one that changes a label, because it sees only a
// digest of both fields together.
func TestFrostProofChangesOnAnyRecordEdit(t *testing.T) {
	resolver, store, share, channelID := resolverFixture(t, nil)
	ctx := context.Background()

	before, err := resolver.Enter(ctx, "atmprojekt", "przetarg-2026")
	if err != nil {
		t.Fatal(err)
	}

	edited := share
	edited.Objects = []manifest.Object{{
		PublicID:    share.Objects[0].PublicID,
		RepoPath:    share.Objects[0].RepoPath,
		DisplayName: strings.Repeat("x", 8) + ".pdf",
	}}
	if _, _, err := store.Update(uuid.NewString(), share.OwnerRealm, channelID, edited); err != nil {
		t.Fatal(err)
	}

	after, err := resolver.Enter(ctx, "atmprojekt", "przetarg-2026")
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision {
		t.Fatalf("the fixture must hold the revision still, otherwise this measures the wrong thing: %d then %d", before.Revision, after.Revision)
	}
	if after.FrostProof == before.FrostProof {
		t.Fatal("a display-name edit left the proof unchanged; if that is now true, the coupling described in reports/ has changed and the asymmetry test above needs rewriting")
	}
}
