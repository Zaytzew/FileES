//go:build !windows && !nocfapi

package main

import (
	"context"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

func (m *anchorManager) wireTreeMove(*ipcserver.Server)                                            {}
func (m *anchorManager) recoverConnectedTreeMove(context.Context, localrepo.Record, *anchorBridge) {}
