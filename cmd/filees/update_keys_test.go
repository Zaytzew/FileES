//go:build linux

package main

import (
	"encoding/base64"
	"runtime"
	"strings"
	"testing"

	"filees/pkg/config"
	"filees/pkg/ipcserver"
)

func TestSourceBuildDoesNotEnableClientUpdateWithPlaceholderKey(t *testing.T) {
	if keys, configured := clientReleaseKeyring(); configured || len(keys) != 0 {
		t.Fatalf("placeholder keyring configured: %+v", keys)
	}
	if err := configureClientUpdate(ipcserver.New(t.TempDir()+"/ipc.sock"), nil, "dev"); err != nil {
		t.Fatal(err)
	}
	update := &config.UpdateConfig{Platform: runtime.GOOS + "-" + runtime.GOARCH, StageRoot: t.TempDir()}
	err := configureClientUpdate(ipcserver.New(t.TempDir()+"/ipc.sock"), update, "dev")
	if err == nil || !strings.Contains(err.Error(), "no production release key") {
		t.Fatalf("configured update with placeholder key: %v", err)
	}
}

func TestInjectedPublicReleaseKeyOverridesPlaceholderWithoutTrustFromConfig(t *testing.T) {
	previousKey, previousID := injectedClientReleasePublicKeyB64, injectedClientReleaseKeyID
	defer func() { injectedClientReleasePublicKeyB64, injectedClientReleaseKeyID = previousKey, previousID }()
	publicKey := []byte("untrusted comment: test release key\nRWT012345678901234567890123456789012345678901234567890123456789==\n")
	injectedClientReleasePublicKeyB64 = base64.StdEncoding.EncodeToString(publicKey)
	injectedClientReleaseKeyID = "release-test-1"
	keys, configured := clientReleaseKeyring()
	if !configured || string(keys["release-test-1"]) != strings.TrimSpace(string(publicKey)) {
		t.Fatalf("injected keyring = %#v, configured=%v", keys, configured)
	}
	for _, invalid := range []string{"", "../key", " key", "key/other"} {
		injectedClientReleaseKeyID = invalid
		if _, configured := clientReleaseKeyring(); configured {
			t.Fatalf("accepted injected key ID %q", invalid)
		}
	}
}
