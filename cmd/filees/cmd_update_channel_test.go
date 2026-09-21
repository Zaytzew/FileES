//go:build linux || windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/clientupdate"
	"filees/internal/releaseenvelope"
	"filees/pkg/config"
)

func TestUpdateChannelSurvivesBinaryDefaultsAndKeepsRollbackFloor(t *testing.T) {
	previousRepo, previousChannel := injectedClientReleaseRepoURL, injectedClientReleaseChannel
	defer func() { injectedClientReleaseRepoURL, injectedClientReleaseChannel = previousRepo, previousChannel }()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	injectedClientReleaseRepoURL, injectedClientReleaseChannel = "https://releases.example.test", "alpha"
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte(`{"repositories":[],"server_display_name":"My configuration"}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runUpdateChannel([]string{"--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "configured_channel: alpha") {
		t.Fatal(out.String())
	}
	afterRead, _ := os.ReadFile(path)
	if !bytes.Equal(afterRead, original) {
		t.Fatal("query modified configuration")
	}
	defaults, err := distributionClientUpdateConfig()
	if err != nil {
		t.Fatal(err)
	}
	store := clientupdate.StateStore{Path: defaults.StatePath}
	if err := store.Save(clientupdate.State{HighestSequence: 1500, SecurityEpoch: 2, ReleaseID: "r1500"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(store.Path)
	out.Reset()
	if err := runUpdateChannel([]string{"beta", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Restart FileES") {
		t.Fatal("missing restart notice")
	}
	// Re-read as a new process would after an update: explicit configuration
	// wins even if the replacement binary carries a different default channel.
	injectedClientReleaseChannel = "stable"
	view, err := config.LoadClientView(path)
	if err != nil {
		t.Fatal(err)
	}
	if !view.UpdateConfigured || view.Update.Channel != "beta" || view.Update.StatePath != store.Path {
		t.Fatalf("update=%+v", view.Update)
	}
	out.Reset()
	if err := runUpdateChannel([]string{"--config", path}, &out); err != nil || !strings.Contains(out.String(), "configured_channel: beta") {
		t.Fatalf("%v: %s", err, out.String())
	}
	after, _ := os.ReadFile(store.Path)
	if !bytes.Equal(before, after) {
		t.Fatal("channel selection modified anti-rollback state")
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Check(&releaseenvelope.Envelope{Sequence: 1499, SecurityEpoch: 2, ReleaseID: "older-beta"}) == nil {
		t.Fatal("channel switch reset rollback protection")
	}
}

func TestUpdateChannelDoesNotEnableDisabledUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte(`{"repositories":[],"update":{"enabled":false}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runUpdateChannel([]string{"beta", "--config", path}, &out); err == nil {
		t.Fatal("enabled updates")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(original, after) {
		t.Fatal("opt-out changed")
	}
}

func TestChannelConfigRefusesConcurrentEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := replaceChannelConfig(path, []byte("old"), []byte("new"), 0600); err == nil {
		t.Fatal("overwrote observed concurrent change")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "changed" {
		t.Fatal("lost concurrent change")
	}
}
