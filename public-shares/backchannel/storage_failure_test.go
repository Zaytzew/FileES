package backchannel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"filees/public-shares/authority"
	"filees/public-shares/storage"
)

type unavailableAuthority struct{ stubAuthority }

func (unavailableAuthority) Fetch(context.Context, authority.ObjectRequest) (authority.FetchedLeaf, error) {
	return authority.FetchedLeaf{}, fmt.Errorf("%w: secret-path", storage.ErrUnavailable)
}
func TestStorageFailureSurvivesBackchannelWithoutPrivateDetails(t *testing.T) {
	client := Client{BaseURL: "http://authority", HTTP: &http.Client{Transport: handlerTransport{Server{Authority: unavailableAuthority{}}}}}
	_, err := client.Fetch(context.Background(), authority.ObjectRequest{ChannelID: "channel", PublicID: "object", Revision: 1, FrostProof: "proof"})
	if !errors.Is(err, storage.ErrUnavailable) || err.Error() != storage.ErrUnavailable.Error() {
		t.Fatalf("lost/overexposed storage failure: %v", err)
	}
}
