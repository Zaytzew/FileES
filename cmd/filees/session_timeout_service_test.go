package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientprofile"
)

func TestSessionTimeoutServicePersistsMinutes(t *testing.T) {
	root := t.TempDir()
	serverID := "office"
	profile := clientprofile.Profile{
		Schema: clientprofile.Schema, ServerID: serverID, DisplayName: "Office", Address: "example.net",
		ClientID: "00000000-0000-0000-0000-000000000001", IdentityFile: filepath.Join(root, "id"),
		KnownHosts: filepath.Join(root, "known"), SSHPort: 22,
		ServiceURL: "svn+ssh://_filees-client@example.net/", ServiceWC: filepath.Join(root, "wc"),
		RelativeViewPath: "view.json", CachePath: filepath.Join(root, "cache.json"), PollInterval: time.Minute,
	}
	path := filepath.Join(root, serverID, "client-profile.json")
	if err := clientprofile.Store(path, profile); err != nil {
		t.Fatal(err)
	}
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
