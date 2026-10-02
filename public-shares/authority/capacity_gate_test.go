package authority

import (
	"context"
	"errors"
	"os"
	"testing"

	"filees/public-shares/storage"
)

func TestCapacityStopsMaterializationButKeepsPublicListing(t *testing.T) {
	r, _, _, _ := resolverFixture(t, nil)
	entry, err := r.Enter(context.Background(), "atmprojekt", "przetarg-2026")
	if err != nil {
		t.Fatal(err)
	}
	r.WithWrites = func(context.Context, func() error) error { return errors.New("capacity stop") }
	if _, err := r.Inspect("atmprojekt", "przetarg-2026"); err != nil {
		t.Fatal("listing denied", err)
	}
	request := ObjectRequest{ChannelID: entry.Projection.ChannelID, PublicID: "7f3a1c9e2b4d6a80", Revision: entry.Revision, FrostProof: entry.FrostProof}
	if _, err := r.Fetch(context.Background(), request); !errors.Is(err, storage.ErrUnavailable) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(r.StagingRoot)
	if err != nil || len(entries) != 0 {
		t.Fatal("materialization started under stop", entries, err)
	}
}
