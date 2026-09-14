//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorePathsOutsidePackage(t *testing.T) {
	home := t.TempDir()
	install := filepath.Join(t.TempDir(), "WindowsApps", "FileES")
	paths := pathsFor(home, install)
	if !strings.HasPrefix(paths.config, home+string(filepath.Separator)) {
		t.Fatalf("config path is outside user home: %q", paths.config)
	}
	if strings.HasPrefix(paths.config, install) || strings.HasPrefix(paths.logs, install) {
		t.Fatalf("Store writes would enter immutable package: %#v", paths)
	}
	if paths.daemon != filepath.Join(install, "filees.exe") || paths.supervisor != filepath.Join(install, "filees-store-startup.exe") {
		t.Fatalf("incorrect package executables: %#v", paths)
	}
}

func TestEnsureConfigCreatesOnceWithoutReplacingUserChanges(t *testing.T) {
	home := t.TempDir()
	path := pathsFor(home, t.TempDir()).config
	if err := ensureConfig(path, home); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var seed struct {
		Transport struct {
			IdentityFile string `json:"identity_file"`
			KnownHosts   string `json:"known_hosts"`
		} `json:"transport"`
		Repositories []any `json:"repositories"`
	}
	if err := json.Unmarshal(data, &seed); err != nil {
		t.Fatal(err)
	}
	if seed.Transport.IdentityFile != filepath.Join(home, ".local", "share", "filees", "identity", "id_ed25519") ||
		seed.Transport.KnownHosts != filepath.Join(home, ".local", "share", "filees", "known_hosts") ||
		seed.Repositories == nil || len(seed.Repositories) != 0 {
		t.Fatalf("incorrect seed: %#v", seed)
	}
	custom := []byte(`{"repositories":[{"id":"user-owned"}]}`)
	if err := os.WriteFile(path, custom, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureConfig(path, home); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(custom) {
		t.Fatalf("user config overwritten: %s", got)
	}
}

func TestRefuseLegacyMSI(t *testing.T) {
	root := t.TempDir()
	if err := refuseLegacyMSI(root); err != nil {
		t.Fatalf("empty isolated profile should be allowed: %v", err)
	}
	legacy := filepath.Join(root, "Programs", "FileES", "filees.exe")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := refuseLegacyMSI(root); err == nil {
		t.Fatal("Store launcher accepted an existing MSI installation")
	}
	if err := refuseLegacyMSI(""); err == nil {
		t.Fatal("Store launcher accepted an unknown legacy location")
	}
	if err := refuseLegacyMSI("relative-path"); err == nil {
		t.Fatal("Store launcher accepted a relative legacy location")
	}
}

func TestUnstampedLauncherFailsClosed(t *testing.T) {
	previous := launcherMode
	launcherMode = ""
	defer func() { launcherMode = previous }()
	if err := run(nil); err == nil {
		t.Fatal("unstamped Store launcher started")
	}
}
