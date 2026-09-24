//go:build nocfapi

package main

import (
	"context"

	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

// explorerAnchors is absent from a nocfapi build: the Microsoft Store package
// ships without Explorer anchors and without any Cloud Files API code (owner's
// decision, 2026-09-24). With no precheck set, repo.anchor_create answers
// anchor_unavailable and the capability is never advertised.
func explorerAnchors(*ipcserver.Server, *localrepo.Store) func(context.Context) { return nil }
