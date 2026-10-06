//go:build !windows && !nocfapi

package main

import (
	"context"
	"filees/pkg/localrepo"
)

type anchorTreeRefresh struct{}

func guardAnchorSeedDirectory(_, _ string, create func() error) error { return create() }

func (m *anchorManager) refreshRemoteTree(context.Context, localrepo.Record, *anchorBridge, *anchorTreeRefresh) {
}
