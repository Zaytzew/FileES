package authority

import (
	"context"
	"io"
	"testing"
	"time"

	"filees/public-shares/storage"
)

type sweepingSource struct {
	Source
	staging *storage.Staging
	t       *testing.T
}

func (s sweepingSource) Cat(ctx context.Context, repo, path string, revision int64, w io.Writer) error {
	result, err := s.staging.Sweep(ctx, time.Now())
	if err != nil || result.Active != 1 || result.Files != 0 {
		s.t.Fatalf("staging not pinned during Cat: %+v %v", result, err)
	}
	return s.Source.Cat(ctx, repo, path, revision, w)
}

func TestResolverPinsStagingFromCreationThroughBodyClose(t *testing.T) {
	r, _, share, _ := resolverFixture(t, nil)
	r.Staging = &storage.Staging{Root: r.StagingRoot}
	r.Source = sweepingSource{Source: r.Source, staging: r.Staging, t: t}
	entry, err := r.Enter(context.Background(), "atmprojekt", "przetarg-2026")
	if err != nil {
		t.Fatal(err)
	}
	request := ObjectRequest{ChannelID: entry.Projection.ChannelID, PublicID: share.Objects[0].PublicID, Revision: entry.Revision, FrostProof: entry.FrostProof}
	leaf, err := r.Fetch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer leaf.Body.Close()
	result, err := r.Staging.Sweep(context.Background(), time.Now())
	if err != nil || result.Active != 1 || result.Files != 0 {
		t.Fatalf("staging not pinned during send: %+v %v", result, err)
	}
	if raw, err := io.ReadAll(leaf.Body); err != nil || string(raw) != "revision five" {
		t.Fatalf("body=%q %v", raw, err)
	}
	if err := leaf.Body.Close(); err != nil {
		t.Fatal(err)
	}
	result, err = r.Staging.Sweep(context.Background(), time.Now())
	if err != nil || result.Active != 0 || result.Files != 0 {
		t.Fatalf("Close leaked staging/pin: %+v %v", result, err)
	}
}
