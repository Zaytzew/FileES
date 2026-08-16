package errmap

import (
	"errors"
	"testing"

	"filees/pkg/errcat"
)

func TestClassifyMobileTreeAndStatus70(t *testing.T) {
	cases := []struct {
		raw string
		key errcat.Key
	}{
		{
			raw: "UPLOAD_TREE: sshtransport: session failed: Process exited with status 70 (response read: read frame magic: EOF)",
			key: errcat.KeyMobileTreeNotIngested,
		},
		{
			raw: "mobile operation failed: op.unsupported: UPLOAD_TREE is not ingested yet",
			key: errcat.KeyMobileTreeNotIngested,
		},
		{
			raw: "sshtransport: session failed: Process exited with status 70 (response read: read frame magic: EOF)",
			key: errcat.KeyMobileOpNotOnServer,
		},
		{
			raw: "UPLOAD_TREE: not a filees tree pack",
			key: errcat.KeyMobileTreeNotAPack,
		},
		{
			raw: "UPLOAD_TREE: mobile operation failed: tree.payload_corrupt: zip sha256 or size does not match the header",
			key: errcat.KeyMobileTreeCorrupt,
		},
	}
	for _, c := range cases {
		got := Classify(errors.New(c.raw))
		if got.Key != c.key {
			t.Fatalf("Classify(%q) = %s, want %s", c.raw, got.Key, c.key)
		}
		if errcat.Polish(string(got.Key)) == "" || errcat.Polish(string(got.Key)) == errcat.Polish("not.a.real.key") {
			t.Fatalf("missing Polish for %s", c.key)
		}
	}
}
