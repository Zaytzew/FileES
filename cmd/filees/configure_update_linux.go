//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"filees/internal/clientupdate"
	"filees/internal/releaseenvelope"
	"filees/internal/serverinstall/svnfetch"
	"filees/pkg/config"
	"filees/pkg/ipcserver"
)

func configureClientUpdate(ipc *ipcserver.Server, update *config.UpdateConfig, currentVersion string) error {
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
	if err := os.Chmod(update.StageRoot, 0o700); err != nil {
		return fmt.Errorf("secure client update staging: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	fetcher := svnfetch.SVN{Program: update.SVNProgram, RepoURL: update.RepoURL, Timeout: 2 * time.Minute}
	verifier := releaseenvelope.Ed25519Verifier{Keys: keys}
	trustedKeys := make([]string, 0, len(keys))
	for keyID := range keys {
		trustedKeys = append(trustedKeys, keyID)
	}
	resolver := &releaseenvelope.Resolver{Fetcher: fetcher, Verifier: verifier, TrustedKeys: trustedKeys}
	installer := clientupdate.LinuxInstaller{
		Stager: clientupdate.BundleStager{Fetcher: fetcher, Root: update.StageRoot},
		Paths:  clientupdate.LinuxPaths{Home: home, Prefix: filepath.Join(home, ".local"), DataHome: dataHome, ConfigHome: configHome},
	}
	service := &clientupdate.Service{
		Resolver: resolver, Installer: installer, State: clientupdate.StateStore{Path: update.StatePath},
		Channel: update.Channel, ChannelPath: "channels/" + update.Channel + ".v2.json", Component: update.Component,
		Platform: update.Platform, CurrentVersion: currentVersion,
	}
	ipc.SetUpdateService(service)
	return nil
}
