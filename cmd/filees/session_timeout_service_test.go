package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientprofile"
)

func TestSessionTimeoutServicePersistsMinutes(t *testing.T) {
	root := t.TempDir()
	path := storeSessionTimeoutProfile(t, root, "office")
	assertSessionTimeoutPersisted(t, root, "office", path)
}

// The service joined the raw server ID into the profile path, so on the
// owner's Windows machine "Limit czasu wysyłki i pobierania → Zmień" failed for
// "atmprojekt:filees": its profile lives in "atmprojekt+3Afilees". The same
// defect was found live in the history service on 2026-09-15.
func TestSessionTimeoutServiceUsesTheEncodedServerDir(t *testing.T) {
	root := t.TempDir()
	const serverID = "atmprojekt:filees"
	path := storeSessionTimeoutProfile(t, root, serverID)
	if !strings.Contains(filepath.ToSlash(path), "+3A") {
		t.Fatalf("this test proves nothing unless the ID is actually encoded: %q", path)
	}
	assertSessionTimeoutPersisted(t, root, serverID, path)
}

func storeSessionTimeoutProfile(t *testing.T, root, serverID string) string {
	t.Helper()
	dir, err := clientprofile.ServerDir(root, serverID)
	if err != nil {
		t.Fatal(err)
	}
	profile := clientprofile.Profile{
		Schema: clientprofile.Schema, ServerID: serverID, DisplayName: "Office", Address: "example.net",
		ClientID: "00000000-0000-0000-0000-000000000001", IdentityFile: filepath.Join(dir, "id"),
		KnownHosts: filepath.Join(dir, "known"), SSHPort: 22,
		ServiceURL: "svn+ssh://_filees-client@example.net/", ServiceWC: filepath.Join(dir, "wc"),
		RelativeViewPath: "view.json", CachePath: filepath.Join(dir, "cache.json"), PollInterval: time.Minute,
	}
	path := filepath.Join(dir, "client-profile.json")
	if err := clientprofile.Store(path, profile); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertSessionTimeoutPersisted(t *testing.T, root, serverID, path string) {
	t.Helper()
	var seen clientprofile.Profile
	service := sessionTimeoutService{root: root, onChange: func(got clientprofile.Profile) { seen = got }}
	minutes, err := service.SetSessionTimeout(context.Background(), serverID, 90)
	if err != nil || minutes != 90 {
		t.Fatalf("set: minutes=%d err=%v", minutes, err)
	}
	loaded, err := clientprofile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SVNTimeout() != 90*time.Minute || seen.SVNTimeout() != 90*time.Minute {
		t.Fatalf("persisted=%v notified=%v", loaded.SessionTimeout, seen.SessionTimeout)
	}
}
