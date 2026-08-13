package main

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"strings"

	"filees/internal/releaseenvelope"
)

const embeddedClientReleaseKeyID = "release-2026-a"

// Set only by the trusted build pipeline through -ldflags -X. Encoding the
// public (never secret) key avoids whitespace/newline ambiguity in argv.
var injectedClientReleasePublicKeyB64 string
var injectedClientReleaseKeyID string

//go:embed assets/release.pub
var embeddedClientReleasePublicKey []byte

func clientReleaseKeyring() (map[string][]byte, bool) {
	keyID := embeddedClientReleaseKeyID
	key := embeddedClientReleasePublicKey
	if encoded := strings.TrimSpace(injectedClientReleasePublicKeyB64); encoded != "" {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, false
		}
		key = decoded
		keyID = injectedClientReleaseKeyID
	}
	if bytes.Contains(key, []byte("PLACEHOLDER")) || bytes.Contains(key, []byte("xxxx")) || !validReleaseKeyID(keyID) {
		return nil, false
	}
	canonical, err := releaseenvelope.CanonicalSignifyPublicKey(key)
	if err != nil {
		return nil, false
	}
	return map[string][]byte{keyID: canonical}, true
}

func validReleaseKeyID(value string) bool {
	if value == "" || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for index, character := range value {
		if index == 0 && !asciiAlphanumeric(character) {
			return false
		}
		if asciiAlphanumeric(character) || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func asciiAlphanumeric(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
}
