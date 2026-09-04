//go:build linux

package main

import (
	"bytes"
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
	if err := configureClientUpdate(ipcserver.New(t.TempDir()+"/ipc.sock"), nil, false, "dev"); err != nil {
		t.Fatal(err)
	}
	update := &config.UpdateConfig{Platform: runtime.GOOS + "-" + runtime.GOARCH, StageRoot: t.TempDir()}
	err := configureClientUpdate(ipcserver.New(t.TempDir()+"/ipc.sock"), update, true, "dev")
	if err == nil || !strings.Contains(err.Error(), "no production release key") {
		t.Fatalf("configured update with placeholder key: %v", err)
	}
}

func TestInjectedPublicReleaseKeyOverridesPlaceholderWithoutTrustFromConfig(t *testing.T) {
	previousKey, previousID := injectedClientReleasePublicKeyB64, injectedClientReleaseKeyID
	defer func() { injectedClientReleasePublicKeyB64, injectedClientReleaseKeyID = previousKey, previousID }()
	rawKey := make([]byte, 42)
	copy(rawKey, "Ed")
	publicKey := []byte("untrusted comment: test release key\n" + base64.StdEncoding.EncodeToString(rawKey) + "\n")
	transportKey := bytes.TrimSuffix(bytes.ReplaceAll(publicKey, []byte("\n"), []byte("\r\n")), []byte("\r\n"))
	injectedClientReleasePublicKeyB64 = base64.StdEncoding.EncodeToString(transportKey)
	injectedClientReleaseKeyID = "release-test-1"
	keys, configured := clientReleaseKeyring()
	if !configured || string(keys["release-test-1"]) != string(publicKey) {
		t.Fatalf("injected keyring = %#v, configured=%v", keys, configured)
	}
	for _, invalid := range []string{"", "../key", " key", "key/other"} {
		injectedClientReleaseKeyID = invalid
		if _, configured := clientReleaseKeyring(); configured {
			t.Fatalf("accepted injected key ID %q", invalid)
		}
	}
}
