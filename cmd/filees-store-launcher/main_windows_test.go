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

// With an MSI installation present the Store version never starts a second
// pair: at logon it yields to the MSI's own autostart, and when the user opens
// it, it asks to remove the MSI. An unknown or relative location is an error,
// not "no MSI", because guessing wrong there starts two daemons.
func TestMSIStep(t *testing.T) {
	root := t.TempDir()
	for _, mode := range []string{"interactive", "startup"} {
		if step, err := msiStep(root, mode); err != nil || step != noPredecessor {
			t.Fatalf("%s without MSI: step=%v err=%v", mode, step, err)
		}
	}
	legacy := filepath.Join(root, "Programs", "FileES", "filees.exe")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if step, err := msiStep(root, "startup"); err != nil || step != yieldToMSI {
		t.Fatalf("startup with MSI: step=%v err=%v, want yield", step, err)
	}
	if step, err := msiStep(root, "interactive"); err != nil || step != askToReplaceMSI {
		t.Fatalf("interactive with MSI: step=%v err=%v, want ask", step, err)
	}
	if _, err := msiStep("", "interactive"); err == nil {
		t.Fatal("Store launcher accepted an unknown legacy location")
	}
	if _, err := msiStep("relative-path", "interactive"); err == nil {
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
