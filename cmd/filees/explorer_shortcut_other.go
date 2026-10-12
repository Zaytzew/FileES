//go:build !windows && !nocfapi

package main

import (
	"context"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

func (m *anchorManager) wireExplorerShortcut(*ipcserver.Server) {}
func (m *anchorManager) registerShellPreference(context.Context, localrepo.Record, string) error {
	return nil
}
