//go:build !nocfapi

package main

import (
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
	m := &anchorManager{options: loadAnchorOptions(filepath.Join(root, "virtual.json")), lifecycle: store, localIntentMu: &sync.Mutex{}}
	if m.virtualEnabled() {
		t.Fatal("default enabled")
	}
	t.Setenv("FILEES_CFAPI_SHELL_PREVIEW", "1")
	if m.virtualEnabled() {
		t.Fatal("environment opted in")
	}
	yes, no := true, false
	if _, err = m.VirtualFolders(t.Context(), &yes); err != nil {
		t.Fatal(err)
	}
	if !loadAnchorOptions(m.options.path).enabled.Load() {
		t.Fatal("setting not durable")
	}
	if _, err = m.VirtualFolders(t.Context(), &no); err != nil || m.virtualEnabled() || loadAnchorOptions(m.options.path).enabled.Load() {
		t.Fatalf("empty installation could not disable durably: %v", err)
	}
	if _, err = m.VirtualFolders(t.Context(), &yes); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginAnchorAttach("test", "repo", filepath.Join(root, "point"), false); err != nil {
		t.Fatal(err)
	}
	if _, err = m.VirtualFolders(t.Context(), &no); err == nil || !m.virtualEnabled() {
		t.Fatal("pending point lost preference")
	}
	if !loadAnchorOptions(m.options.path).enabled.Load() {
		t.Fatal("refusal changed file")
	}
	m.localIntentMu.Lock()
	if _, err = m.VirtualFolders(t.Context(), &no); err == nil {
		t.Fatal("concurrent local intent admitted")
	}
	m.localIntentMu.Unlock()
}

func TestDisabledVirtualFoldersRefusesLifecycleBeforeAnyRecord(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := repositoryLifecycleService{store: store, anchorEnabled: func() bool { return false }}
	if _, err := service.BeginAnchorAttach("server", "repo", filepath.Join(t.TempDir(), "point"), false); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled point accepted: %v", err)
	}
}

func TestVirtualFoldersCorruptPreferenceNeverEnablesOrOverwrites(t *testing.T) {
	for _, raw := range []string{`{"schema":"wrong","enabled":true}`, `{"schema":"filees.virtual-folders/v1","enabled":true,"anything":1}`, `{"schema":"filees.virtual-folders/v1","enabled":true} {}`, strings.Repeat(" ", 5000)} {
		path := filepath.Join(t.TempDir(), "virtual.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		m := &anchorManager{options: loadAnchorOptions(path), localIntentMu: &sync.Mutex{}}
		if m.virtualEnabled() || m.options.readErr == nil {
			t.Fatal("corruption enabled points")
		}
		yes := true
		if _, err := m.VirtualFolders(t.Context(), &yes); err == nil {
			t.Fatal("overwrote corrupt preference")
		}
		got, _ := os.ReadFile(path)
		if string(got) != raw {
			t.Fatal("file changed")
		}
	}
}
