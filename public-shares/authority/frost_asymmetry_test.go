package authority

import (
	"context"
	"strings"
	"testing"

	"filees/public-shares/manifest"

	"github.com/google/uuid"
)

// The asymmetry this file records is still present at this layer and is left
// alone on purpose: the object path recomputing the proof is what makes a
// revoked channel stop serving bytes, and loosening it here would remove that.
// What was wrong was letting it decide a visitor's fate, and that is answered
// one layer up - public-shares/web refreshes a live visit instead of refusing
// it. The invariant test lives there, in TestLiveLinkSurvivesAManifestEditMidVisit,
// because it is a property of the link and not of the resolver.
//
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
