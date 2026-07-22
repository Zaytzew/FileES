package main

import (
	"bytes"
	_ "embed"
)

const embeddedClientReleaseKeyID = "release-2026-a"

//go:embed assets/release.pub
var embeddedClientReleasePublicKey []byte

func clientReleaseKeyring() (map[string][]byte, bool) {
	key := bytes.TrimSpace(embeddedClientReleasePublicKey)
	configured := len(key) > 20 && !bytes.Contains(key, []byte("PLACEHOLDER")) && !bytes.Contains(key, []byte("xxxx"))
	if !configured {
		return nil, false
	}
	return map[string][]byte{embeddedClientReleaseKeyID: append([]byte(nil), embeddedClientReleasePublicKey...)}, true
}
