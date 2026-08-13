package main

import _ "embed"

import (
	"bytes"
	"encoding/base64"
	"strings"
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
	key = bytes.TrimSpace(key)
	if bytes.Contains(key, []byte("xxxx")) || bytes.Contains(key, []byte("PLACEHOLDER")) {
		return nil, false
	}
	lines := splitLines(key)
	if len(lines) != 2 || !bytes.HasPrefix(lines[0], []byte("untrusted comment:")) {
		return nil, false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(lines[1])))
	// signify public keys encode the two-byte "Ed" algorithm marker, an
	// eight-byte key ID and a 32-byte Ed25519 public key.
	if err != nil || len(decoded) != 42 || decoded[0] != 'E' || decoded[1] != 'd' {
		return nil, false
	}
	return append([]byte(nil), key...), true
}

func pubkeyConfigured() bool {
	_, ok := releasePubkey()
	return ok
}

func splitLines(b []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			lines = append(lines, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		lines = append(lines, b[start:])
	}
	return lines
}

func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
outer:
	for i := 0; i <= len(haystack)-len(needle); i++ {
		for j := range needle {
			if haystack[i+j] != needle[j] {
				continue outer
			}
		}
		return true
	}
	return false
}
