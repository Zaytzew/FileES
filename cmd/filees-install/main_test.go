package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"filees/internal/serverinstall/config"
)

func TestReleasePubkeyInjectionRequiresSignifyPublicKey(t *testing.T) {
	original := injectedServerReleasePublicKeyB64
	t.Cleanup(func() { injectedServerReleasePublicKeyB64 = original })

	injectedServerReleasePublicKeyB64 = base64.StdEncoding.EncodeToString([]byte("untrusted comment: header only"))
	if _, ok := releasePubkey(); ok {
		t.Fatal("header-only input accepted as a release public key")
	}
	rawKey := make([]byte, 42)
	copy(rawKey, "Ed")
	public := "untrusted comment: FileES release test\n" + base64.StdEncoding.EncodeToString(rawKey)
	injectedServerReleasePublicKeyB64 = base64.StdEncoding.EncodeToString([]byte(public))
	got, ok := releasePubkey()
	if !ok || string(got) != public {
		t.Fatalf("valid injected public key rejected: ok=%v key=%q", ok, got)
	}
}

func TestSelectActionIncludesAdopt(t *testing.T) {
	action, releaseID, _, err := selectAction(&config.Config{DefaultAction: "check"}, actionFlags{adopt: true, args: []string{"r7"}})
	if err != nil || action != "adopt" || releaseID != "r7" {
		t.Fatalf("adopt selection = %q %q %v", action, releaseID, err)
	}
	_, _, _, err = selectAction(&config.Config{DefaultAction: "check"}, actionFlags{adopt: true, apply: true})
	if err == nil || !strings.Contains(err.Error(), "--adopt") {
		t.Fatalf("conflicting adopt was not rejected: %v", err)
	}
}
