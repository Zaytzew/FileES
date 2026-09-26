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
			// The op prefix must not change this classification: a bare
			// status-70 dispatch failure means the same thing whether it
			// happened during UPLOAD_TREE or any other operation. Before
			// the fix, mobileTreeNeedles' bare "upload_tree" needle matched
			// this text purely because of the op name in the client's own
			// error-wrapping prefix, confidently misreporting an unrelated
			// status-70 dispatch failure as "server cannot ingest trees".
			raw: "UPLOAD_TREE: sshtransport: session failed: Process exited with status 70 (response read: read frame magic: EOF)",
			key: errcat.KeyMobileOpNotOnServer,
		},
		{
			// Live, 2026-09-26, against spot.example.net (confirmed at HEAD,
			// not an old-server case): an abrupt SSH channel close mid
			// UPLOAD_TREE, previously also misclassified as "not ingested"
			// by the same over-broad needle.
			raw: "Upload_tree: sshtransport: session failed: wait: remote command exited without exit status or exit signal (response read: read frame magic: EOF)",
			key: errcat.KeyConnectionDropped,
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
		// The sentence a person reads comes from the language packs and is
		// gated there. Here the requirement is a real dictionary entry with
		// its own diagnostic, not the unknown fallback.
		if !errcat.KnownKey(string(got.Key)) || errcat.Diagnostic(string(got.Key)) == errcat.Diagnostic("not.a.real.key") {
			t.Fatalf("%s did not resolve to a distinct dictionary entry", c.key)
		}
	}
}
