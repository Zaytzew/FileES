//go:build linux || windows

package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"filees/pkg/clientprofile"
	"filees/pkg/config"
)

// distributionClientUpdateConfig turns immutable build metadata into an
// opt-out update service. Both values are required together: a half-configured
// release must fail at startup rather than silently claiming to auto-update.
//
// Shared by both desktop platforms. It used to live with the Windows wiring
// only, so a Linux client - built with the same injected channel - updated
// solely when someone had written an update section into its config by hand,
// and the shipped config never had one: every Linux installation stayed on the
// version it was installed with.
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
