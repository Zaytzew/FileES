//go:build !nocfapi

package main

import (
	"filees/pkg/clientprofile"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"filees/pkg/localrepo"
)

func TestVirtualFoldersDefaultDurableAndDisableFence(t *testing.T) {
	root := t.TempDir()
	store, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := virtualFolderManagerFixture(t, root, store)
	if m.virtualEnabled("test") {
		t.Fatal("default enabled")
	}
	t.Setenv("FILEES_CFAPI_SHELL_PREVIEW", "1")
	if m.virtualEnabled("test") {
		t.Fatal("environment opted in")
	}
	yes, no := true, false
	if _, err = m.VirtualFolders(t.Context(), "test", &yes); err != nil {
		t.Fatal(err)
	}
	if !loadAnchorOptions(m.options.path).servers.Load().(map[string]bool)["test"] {
		t.Fatal("setting not durable")
	}
	if _, err = m.VirtualFolders(t.Context(), "test", &no); err != nil || m.virtualEnabled("test") || loadAnchorOptions(m.options.path).servers.Load().(map[string]bool)["test"] {
		t.Fatalf("empty installation could not disable durably: %v", err)
	}
	if _, err = m.VirtualFolders(t.Context(), "test", &yes); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginAnchorAttach("test", "repo", filepath.Join(root, "point"), false); err != nil {
		t.Fatal(err)
	}
	if _, err = m.VirtualFolders(t.Context(), "test", &no); err == nil || !m.virtualEnabled("test") {
		t.Fatal("pending point lost preference")
	}
	if !loadAnchorOptions(m.options.path).servers.Load().(map[string]bool)["test"] {
		t.Fatal("refusal changed file")
	}
	m.localIntentMu.Lock()
	if _, err = m.VirtualFolders(t.Context(), "test", &no); err == nil {
		t.Fatal("concurrent local intent admitted")
	}
	m.localIntentMu.Unlock()
}

func TestDisabledVirtualFoldersRefusesLifecycleBeforeAnyRecord(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := repositoryLifecycleService{store: store, anchorEnabled: func(string) bool { return false }}
	if _, err := service.BeginAnchorAttach("server", "repo", filepath.Join(t.TempDir(), "point"), false); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled point accepted: %v", err)
	}
}

func TestVirtualFoldersCorruptPreferenceNeverEnablesOrOverwrites(t *testing.T) {
	for _, raw := range []string{`{"schema":"wrong","enabled":true}`, `{"schema":"filees.virtual-folders/v1","enabled":true,"anything":1}`, `{"schema":"filees.virtual-folders/v1","enabled":true} {}`, `{"schema":"filees.virtual-folders/v2","enabled":true}`, `{"schema":"filees.virtual-folders/v2","servers":{"../other":true}}`, `{"schema":"filees.virtual-folders/v1","servers":{"test":true}}`, strings.Repeat(" ", 65537)} {
		path := filepath.Join(t.TempDir(), "virtual.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		m := virtualFolderManagerFixture(t, filepath.Dir(path), nil)
		m.options = loadAnchorOptions(path)
		if m.virtualEnabled("test") || m.options.readErr == nil {
			t.Fatal("corruption enabled points")
		}
		yes := true
		if _, err := m.VirtualFolders(t.Context(), "test", &yes); err == nil {
			t.Fatal("overwrote corrupt preference")
		}
		got, _ := os.ReadFile(path)
		if string(got) != raw {
			t.Fatal("file changed")
		}
	}
}

func virtualFolderManagerFixture(t *testing.T, root string, store *localrepo.Store) *anchorManager {
	t.Helper()
	m := &anchorManager{profiles: filepath.Join(root, "servers"), options: loadAnchorOptions(filepath.Join(root, "virtual.json")), lifecycle: store, localIntentMu: &sync.Mutex{}}
	for _, serverID := range []string{"test", "other"} {
		dir, err := clientprofile.ServerDir(m.profiles, serverID)
		if err != nil {
			t.Fatal(err)
		}
		if err = clientprofile.Store(filepath.Join(dir, "client-profile.json"), passportFixtureProfile(t, serverID)); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestVirtualFoldersServersAreIndependentAndUnknownRefused(t *testing.T) {
	root := t.TempDir()
	store, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := virtualFolderManagerFixture(t, root, store)
	yes, no := true, false
	for _, serverID := range []string{"test", "other"} {
		if _, err = m.VirtualFolders(t.Context(), serverID, &yes); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.BeginAnchorAttach("other", "repo", filepath.Join(root, "point"), false); err != nil {
		t.Fatal(err)
	}
	if _, err = m.VirtualFolders(t.Context(), "test", &no); err != nil {
		t.Fatal("other server blocked disabling", err)
	}
	if m.virtualEnabled("test") || !m.virtualEnabled("other") {
		t.Fatal("coupled settings")
	}
	if err = store.CheckAnchorRetirement(); err == nil {
		t.Fatal("global update guard lost")
	}
	before, _ := os.ReadFile(m.options.path)
	for _, serverID := range []string{"", "unknown", "../other"} {
		if _, err = m.VirtualFolders(t.Context(), serverID, &yes); err == nil {
			t.Fatal("unknown scope accepted", serverID)
		}
	}
	after, _ := os.ReadFile(m.options.path)
	if string(before) != string(after) {
		t.Fatal("refusal changed preference")
	}
	m.options = loadAnchorOptions(m.options.path)
	if m.virtualEnabled("test") || !m.virtualEnabled("other") {
		t.Fatal("restart lost isolation")
	}
	state, err := m.VirtualFolders(t.Context(), "", nil)
	if err != nil || state.Enabled || !state.Servers["other"] {
		t.Fatal(state, err)
	}
	state.Servers["other"] = false
	if !m.virtualEnabled("other") {
		t.Fatal("summary shared mutable state")
	}
}

func TestVirtualFoldersLegacyMigrationPreservesOnlyExistingPoints(t *testing.T) {
	for _, legacy := range []string{"true", "false"} {
		root := t.TempDir()
		store, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
		if err != nil {
			t.Fatal(err)
		}
		m := virtualFolderManagerFixture(t, root, store)
		raw := `{"schema":"filees.virtual-folders/v1","enabled":` + legacy + `}`
		if err = os.WriteFile(m.options.path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		records := []localrepo.Record{{ServerID: "other", Anchor: true, State: localrepo.StateAttached}, {ServerID: "test", Anchor: true, State: localrepo.StateDetached}}
		m.options = loadAnchorOptions(m.options.path, records...)
		if m.virtualEnabled("test") || m.virtualEnabled("other") != (legacy == "true") {
			t.Fatal("migration broadened opt-in")
		}
		unchanged, _ := os.ReadFile(m.options.path)
		if string(unchanged) != raw {
			t.Fatal("read rewrote preference")
		}
		yes := true
		if _, err = m.VirtualFolders(t.Context(), "test", &yes); err != nil {
			t.Fatal(err)
		}
		m.options = loadAnchorOptions(m.options.path)
		if !m.virtualEnabled("test") || m.virtualEnabled("other") != (legacy == "true") {
			t.Fatal("v2 lost migrated state")
		}
	}
}
