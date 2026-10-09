//go:build !windows && !nocfapi

package main

import (
	"context"
	"filees/pkg/localrepo"
)

func (m *anchorManager) bindFileDelete(localrepo.Record, *anchorBridge, *anchorHelperStreams) func() {
	return func() {}
}
func (m *anchorManager) recoverConnectedFileDelete(context.Context, localrepo.Record) {}
