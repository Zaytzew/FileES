//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"filees/internal/clientupdate"
	"filees/internal/releaseenvelope"
	"filees/internal/serverinstall/svnfetch"
	"filees/pkg/clientprofile"
	"filees/pkg/config"
	"filees/pkg/ipcserver"
)

// configureClientUpdate wires the Windows desktop client to the release channel.
//
// Until now this platform had no self-update at all - the shared stub refused
// outright - so every new build reached the owner because somebody copied files
// onto his machine by hand. That is not a channel, and an alpha that cannot ship
// a fix without a person present is not one either.
//
// The channel itself is the one that already exists and is already signed:
// FILEES-BIN, resolved and verified exactly as the Linux client does it. Only
// the installing differs, because the two platforms lay the product out
// differently, and that difference is the whole of clientupdate.DirectoryInstaller.
func configureClientUpdate(ipc *ipcserver.Server, update *config.UpdateConfig, explicitlyConfigured bool, currentVersion string) error {
	if update == nil && !explicitlyConfigured {
		var err error
		update, err = distributionClientUpdateConfig()
		if err != nil {
			return err
		}
	}
	if update == nil {
		return nil
	}
	wantedPlatform := runtime.GOOS + "-" + runtime.GOARCH
	if update.Platform != wantedPlatform {
		return fmt.Errorf("update platform %q does not match running client %q", update.Platform, wantedPlatform)
	}
	keys, configured := clientReleaseKeyring()
	if !configured {
		return fmt.Errorf("client update is enabled but this build has no production release key")
	}
	if err := os.MkdirAll(update.StageRoot, 0o700); err != nil {
		return fmt.Errorf("prepare client update staging: %w", err)
	}
	installDir, err := clientInstallDirectory()
	if err != nil {
		return err
	}
	fetcher := svnfetch.SVN{Program: update.SVNProgram, RepoURL: update.RepoURL, Timeout: 2 * time.Minute}
	verifier := releaseenvelope.Ed25519Verifier{Keys: keys}
	trustedKeys := make([]string, 0, len(keys))
	for keyID := range keys {
		trustedKeys = append(trustedKeys, keyID)
	}
	resolver := &releaseenvelope.Resolver{Fetcher: fetcher, Verifier: verifier, TrustedKeys: trustedKeys}
	installer := clientupdate.DirectoryInstaller{
		Stager: clientupdate.BundleStager{Fetcher: fetcher, Root: update.StageRoot},
		Paths: clientupdate.DirectoryPaths{
			InstallDir: installDir,
			ConfigPath: filepath.Join(installDir, "config.json"),
		},
	}
	service := &clientupdate.Service{
		Resolver: resolver, Installer: installer, State: clientupdate.StateStore{Path: update.StatePath},
		Channel: update.Channel, ChannelPath: "channels/" + update.Channel + ".v2.json", Component: update.Component,
		Platform: update.Platform, CurrentVersion: currentVersion,
	}
	ipc.SetUpdateService(service)
	return nil
}

// distributionClientUpdateConfig turns immutable build metadata into an
// opt-out update service. Both values are required together: a half-configured
// release must fail at startup rather than silently claiming to auto-update.
func distributionClientUpdateConfig() (*config.UpdateConfig, error) {
	repoURL := strings.TrimSpace(injectedClientReleaseRepoURL)
	channel := strings.TrimSpace(injectedClientReleaseChannel)
	if repoURL == "" && channel == "" {
		return nil, nil
	}
	if repoURL == "" || channel == "" {
		return nil, errors.New("client update distribution defaults require both repository URL and channel")
	}
	root := filepath.Join(filepath.Dir(clientprofile.DefaultRoot()), "update")
	update, err := config.NewUpdateConfig(repoURL, channel, config.DesktopUpdateComponent, runtime.GOOS+"-"+runtime.GOARCH, filepath.Join(root, "state.json"), filepath.Join(root, "stage"), "svn")
	if err != nil {
		return nil, fmt.Errorf("invalid client update distribution defaults: %w", err)
	}
	return &update, nil
}

// clientInstallDirectory is where this daemon is running from.
//
// Taken from the running image rather than from configuration on purpose. A
// configurable install directory is a setting that can point somewhere the
// client is not, and the failure it produces - an update that reports success
// while replacing files nobody runs - is silent and would survive a restart
// looking exactly like a client that refuses to update. The one directory a
// self-updating program can be certain about is its own.
func clientInstallDirectory() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate the running client: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		// A path we cannot resolve is still better than none: EvalSymlinks
		// fails on some network paths, and the unresolved one is what the
		// loader used.
		resolved = executable
	}
	directory := filepath.Dir(filepath.Clean(resolved))
	if strings.TrimSpace(directory) == "" || !filepath.IsAbs(directory) {
		return "", fmt.Errorf("the running client is not in an absolute directory: %q", executable)
	}
	return directory, nil
}
