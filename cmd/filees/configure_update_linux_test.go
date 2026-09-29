//go:build linux

package main

import (
	"strings"
	"testing"
)

// A Linux client built on a release channel updates from it without anyone
// writing an update section into its config. Until r1364 it did not: the
// default lived with the Windows wiring, the shipped Linux config had no
// update section, and every installation stayed on the version it was
// installed with.
//
// The build key is made unusable on purpose so the test stops at the first
// step that needs one: reaching it proves the build defaults were taken up,
// and it keeps the test from staging anything under the real user directory.
func TestLinuxClientTakesTheReleaseChannelItWasBuiltOn(t *testing.T) {
	previousRepo, previousChannel, previousKey := injectedClientReleaseRepoURL, injectedClientReleaseChannel, injectedClientReleasePublicKeyB64
	defer func() {
		injectedClientReleaseRepoURL, injectedClientReleaseChannel, injectedClientReleasePublicKeyB64 = previousRepo, previousChannel, previousKey
	}()
	injectedClientReleaseRepoURL = "svn://cloud.atmprojekt.pl/FILEES-BIN"
	injectedClientReleaseChannel = "alpha"
	injectedClientReleasePublicKeyB64 = "not base64!"

	err := configureClientUpdate(nil, nil, false, "0.1.16.1364", nil)
	if err == nil || !strings.Contains(err.Error(), "no production release key") {
		t.Fatalf("the build's release channel was not taken up: %v", err)
	}

	// An explicit update section - here one that switches updates off - wins
	// over the build default, as on Windows.
	if err := configureClientUpdate(nil, nil, true, "0.1.16.1364", nil); err != nil {
		t.Fatalf("explicit opt-out failed: %v", err)
	}

	// A developer build carries no channel and stays without updates.
	injectedClientReleaseRepoURL, injectedClientReleaseChannel = "", ""
	if err := configureClientUpdate(nil, nil, false, "0.1.16.1364", nil); err != nil {
		t.Fatalf("a build without a channel must not configure updates: %v", err)
	}
}
