//go:build !windows && !nocfapi

package main

import (
	"context"

	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

func (m *anchorManager) recoverAnchorRelease(context.Context, localrepo.Record, *anchorBridge) {}

func (m *anchorManager) wireRelease(*ipcserver.Server) {}

func (m *anchorManager) tickReleaseQueue(context.Context) {}
