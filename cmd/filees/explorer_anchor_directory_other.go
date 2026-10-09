//go:build !windows && !nocfapi

package main

import (
	"context"
	"filees/pkg/localrepo"
)

type anchorDirectoryCursor struct{}

func (m *anchorManager) refreshDirectoryStates(context.Context, localrepo.Record, *anchorBridge, *anchorDirectoryCursor) error {
	return nil
}
