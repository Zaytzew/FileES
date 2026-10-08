//go:build !nocfapi

package main

import "testing"

func TestAnchorProviderBoundaryMustBeVerified(t *testing.T) {
	for _, provider := range []string{"FileES", "Nextcloud", "OneDrive", ""} {
		if err := anchorProviderRefusal(cfapiAnswer{OK: true, Provider: provider}); err == nil {
			t.Errorf("nested provider %q accepted", provider)
		}
	}
	if err := anchorProviderRefusal(cfapiAnswer{Error: "sync_root_boundary", HResult: "0x80070005"}); err == nil {
		t.Fatal("inaccessible boundary mistaken for absence")
	}
	if err := anchorProviderRefusal(cfapiAnswer{Error: "sync_root_info", HResult: "0x80070186"}); err != nil {
		t.Fatalf("ordinary parent refused: %v", err)
	}
}
