//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/config"
)

// The client updates the directory it is running from, and nothing else.
//
// A configurable install directory is a setting that can point somewhere the
// client is not, and the failure that produces is the worst kind: an update
// reports success, replaces files nobody runs, and survives a restart looking
// exactly like a client that refuses to update. The one directory a
// self-updating program can be certain about is its own.
func TestTheClientUpdatesTheDirectoryItRunsFrom(t *testing.T) {
	directory, err := clientInstallDirectory()
	if err != nil {
		t.Fatalf("clientInstallDirectory: %v", err)
	}
	if !filepath.IsAbs(directory) {
		t.Fatalf("directory = %q, want an absolute path", directory)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The test binary is what is running, so its directory is the answer.
	want := filepath.Dir(filepath.Clean(executable))
	if !strings.EqualFold(directory, want) {
		resolved, resolveErr := filepath.EvalSymlinks(executable)
		if resolveErr == nil {
			want = filepath.Dir(filepath.Clean(resolved))
		}
		if !strings.EqualFold(directory, want) {
			t.Fatalf("directory = %q, want %q", directory, want)
		}
	}
}

// The platform gate stays. A config that names another platform must be
// refused loudly rather than updating this one from somebody else's bundle.
func TestAMismatchedUpdatePlatformIsRefused(t *testing.T) {
	update := config.UpdateConfig{Platform: "linux-amd64", Channel: "alpha", Component: "desktop-client"}
	err := configureClientUpdate(nil, &update, true, "0.1.15")
	if err == nil {
		t.Fatal("a linux-amd64 update configuration was accepted by a windows client")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v; the reason must name the mismatch", err)
	}
}

func TestDistributionUpdateDefaultsAndExplicitOptOut(t *testing.T) {
	previousRepo, previousChannel := injectedClientReleaseRepoURL, injectedClientReleaseChannel
	defer func() { injectedClientReleaseRepoURL, injectedClientReleaseChannel = previousRepo, previousChannel }()
	injectedClientReleaseRepoURL = "svn://cloud.atmprojekt.pl/FILEES-BIN/"
	injectedClientReleaseChannel = "alpha"
	update, err := distributionClientUpdateConfig()
	if err != nil {
		t.Fatal(err)
	}
	if update == nil || update.RepoURL != "svn://cloud.atmprojekt.pl/FILEES-BIN" || update.Channel != "alpha" || update.Component != config.DesktopUpdateComponent || !filepath.IsAbs(update.StatePath) || !filepath.IsAbs(update.StageRoot) {
		t.Fatalf("distribution update = %+v", update)
	}
	// A user-owned explicit enabled:false is represented by nil plus true and
	// must win over the build default.
	if err := configureClientUpdate(nil, nil, true, "0.1.15"); err != nil {
		t.Fatalf("explicit opt-out failed: %v", err)
	}
	injectedClientReleaseChannel = ""
	if _, err := distributionClientUpdateConfig(); err == nil {
		t.Fatal("half-configured distribution defaults were accepted")
	}
}

func TestStoreUpdateModeNeverConfiguresFileReplacement(t *testing.T) {
	previous := injectedClientUpdateMode
	defer func() { injectedClientUpdateMode = previous }()
	injectedClientUpdateMode = "store"
	// Even an explicit legacy update configuration must not reach the
	// DirectoryInstaller in a Store build.
	if err := configureClientUpdate(nil, &config.UpdateConfig{Platform: "windows-amd64"}, true, "0.1.16"); err != nil {
		t.Fatalf("Store mode must ignore legacy file replacement: %v", err)
	}
	injectedClientUpdateMode = "typo"
	if err := configureClientUpdate(nil, nil, false, "0.1.16"); err == nil || !strings.Contains(err.Error(), "unknown Windows client update mode") {
		t.Fatalf("unknown distribution mode was accepted: %v", err)
	}
}

func TestWindowsPackageIdentityProbe(t *testing.T) {
	packaged, err := windowsPackageIdentityPresent()
	if err != nil {
		t.Fatal(err)
	}
	if packaged {
		t.Skip("test executable was installed as a package")
	}
}

func TestWindowsPackageIdentityAlwaysGuardsTheFileUpdater(t *testing.T) {
	for _, test := range []struct {
		packaged bool
		mode     string
		allowed  bool
		fails    bool
	}{
		{false, "", true, false},
		{false, "store", false, false},
		{true, "store", false, false},
		{true, "", false, true},
		{true, "typo", false, true},
		{false, "typo", false, true},
	} {
		allowed, err := windowsClientSelfUpdateAllowed(test.packaged, test.mode)
		if allowed != test.allowed || (err != nil) != test.fails {
			t.Errorf("packaged=%t mode=%q: allowed=%t err=%v", test.packaged, test.mode, allowed, err)
		}
	}
}
