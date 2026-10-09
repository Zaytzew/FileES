//go:build !windows && !nocfapi

package main

import "filees/pkg/localrepo"

func (m *anchorManager) bindExplorerRename(localrepo.Record, *anchorBridge, *anchorHelperStreams) {}
