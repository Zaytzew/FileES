package errmap

import (
	"errors"
	"testing"

	"filees/pkg/errcat"
)

func TestClassifyConnectionDroppedAndSessionEnded(t *testing.T) {
	cases := []struct {
		raw string
		key errcat.Key
	}{
		{
			raw: "komenda 'update' zakończyła się błędem: exit status 255\nTimeout, server example.net not responding.",
			key: errcat.KeyConnectionDropped,
		},
		{
			raw: "komenda 'commit' zakończyła się błędem: exit status 1\nfilees-client-entry session: FILEES-SESSION-ENDED: revoked",
			key: errcat.KeySessionEnded,
		},
		{
			raw: "filees-client-entry session: FILEES-SESSION-ENDED: not-allowed",
			key: errcat.KeySessionEnded,
		},
	}
	for _, c := range cases {
		got := Classify(errors.New(c.raw))
		if got.Key != c.key {
			t.Fatalf("Classify(%q) = %s, want %s", c.raw, got.Key, c.key)
		}
		if errcat.Polish(string(got.Key)) == "" {
			t.Fatalf("missing Polish for %s", c.key)
		}
	}
}

func TestConnectionDroppedAndSessionEndedAreNotNetwork(t *testing.T) {
	// recordCommitFailure/recordSyncFailure suppress immediate emission for
	// IsNetwork() entries, deferring to the sustained-offline grace window.
	// Both new keys must stay outside that gate: they represent a connection
	// that was live and then died (or a session the server deliberately
	// ended), not a routine "can't reach the server at all" blip, so they
	// need the prompt notice IsNetwork() would suppress.
	dropped := Classify(errors.New("Timeout, server example.net not responding."))
	if dropped.IsNetwork() {
		t.Fatalf("KeyConnectionDropped must not be IsNetwork(): %+v", dropped)
	}
	ended := Classify(errors.New("FILEES-SESSION-ENDED: revoked"))
	if ended.IsNetwork() {
		t.Fatalf("KeySessionEnded must not be IsNetwork(): %+v", ended)
	}
}
