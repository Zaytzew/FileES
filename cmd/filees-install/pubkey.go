package main

import _ "embed"

import (
	"bytes"
	"encoding/base64"
	"strings"

	"filees/internal/releaseenvelope"
)

// Set only by the trusted build pipeline through -ldflags -X. The value is a
// public key, never secret material; base64 avoids newline ambiguity in argv.
var injectedServerReleasePublicKeyB64 string

//go:embed assets/release.pub
var embeddedPubkey []byte

// pubkeyConfigured returns true if the embedded key looks like a real
// signify public key (not the placeholder shipped in the source tree).
func releasePubkey() ([]byte, bool) {
	key := embeddedPubkey
	if encoded := strings.TrimSpace(injectedServerReleasePublicKeyB64); encoded != "" {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, false
		}
		key = decoded
	}
	if bytes.Contains(key, []byte("xxxx")) || bytes.Contains(key, []byte("PLACEHOLDER")) {
		return nil, false
	}
	canonical, err := releaseenvelope.CanonicalSignifyPublicKey(key)
	if err != nil {
		return nil, false
	}
	return canonical, true
}

func pubkeyConfigured() bool {
	_, ok := releasePubkey()
	return ok
}
