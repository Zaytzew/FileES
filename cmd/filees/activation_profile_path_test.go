package main

import (
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/clientprofile"
	contract "filees/pkg/contract/v1"
)

// Every path built from a server ID goes through ServerDir. This was the last
// site joining it raw, and it sits at the very end of activation, so it failed
// in the most expensive way available: the tunnel writes the identity into the
// encoded directory, this looked in the unencoded one, and the miss surfaced as
// active_profile_pending - a state that returns no error, logs nothing and
// shows the user nothing.
//
// Measured against cloud on 2026-09-03, whose server ID is "atmprojekt:filees".
// On Windows a colon cannot name a directory at all, so the lookup could never
// have succeeded there no matter how well the rest of activation went.
func TestTheProfilePathIsTheOneActivationWroteTo(t *testing.T) {
	root := t.TempDir()
	const serverID = "atmprojekt:filees"

	written, err := clientprofile.ServerDir(root, serverID)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := prepareActivatedClientProfile(t.Context(), contract.ActivationFinishPayload{
		StateRoot: root, ServerID: serverID, ServerAddress: "cloud.atmprojekt.pl:2224",
		KnownHostsPath: filepath.Join(written, "known_hosts"),
	})
	// No identity exists in the fixture, so this must fail - but it has to fail
	// having looked in the directory activation actually uses. A raw join names
	// a path that cannot exist on Windows, and the error says so instead.
	if readErr == nil {
		t.Fatal("an empty state root cannot yield a profile")
	}
	if strings.Contains(readErr.Error(), serverID) {
		t.Fatalf("the unencoded server ID reached the filesystem: %v", readErr)
	}
	if !strings.Contains(filepath.ToSlash(written), "+3A") {
		t.Fatalf("this test proves nothing unless the ID is actually encoded: %q", written)
	}
}
