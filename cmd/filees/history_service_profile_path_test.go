package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientprofile"
)

// Wehikuł czasu joined the raw server ID into the profile path. On the owner's
// Windows machine (2026-09-15, r1290) that made every read of the cloud server
// fail before it started: "atmprojekt:filees" is not a directory name there,
// the profile lives in "atmprojekt+3Afilees".
func TestHistoryReaderLoadsTheProfileFromTheEncodedServerDir(t *testing.T) {
	root := t.TempDir()
	const serverID = "atmprojekt:filees"
	dir, err := clientprofile.ServerDir(root, serverID)
	if err != nil {
		t.Fatal(err)
	}
	profile := clientprofile.Profile{Schema: clientprofile.Schema, ServerID: serverID, DisplayName: "Server", Address: "example.net", ClientID: "00000000-0000-0000-0000-000000000001", IdentityFile: filepath.Join(dir, "id"), KnownHosts: filepath.Join(dir, "known_hosts"), SSHPort: 22, ServiceURL: "svn+ssh://_filees-client@example.net/", ServiceWC: filepath.Join(dir, "wc"), RelativeViewPath: "clients/00000000-0000-0000-0000-000000000001/view.json", CachePath: filepath.Join(dir, "cache"), PollInterval: time.Minute}
	if err := clientprofile.Store(filepath.Join(dir, "client-profile.json"), profile); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.ToSlash(dir), "+3A") {
		t.Fatalf("this test proves nothing unless the ID is actually encoded: %q", dir)
	}

	// No helper is configured, so the reader must still refuse - but only after
	// the profile loaded. A path miss would fail earlier with a different error.
	_, err = historyService{root: root, helper: func() string { return "" }}.reader(serverID)
	if err == nil || !strings.Contains(err.Error(), "native SVN helper is not configured") {
		t.Fatalf("profile was not loaded from %s: %v", dir, err)
	}
}
