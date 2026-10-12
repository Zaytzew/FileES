package main

import (
	"context"
	"errors"
	"filees/internal/gui/actions"
	contract "filees/pkg/contract/v1"
)

type explorerShortcutClient interface {
	RepoExplorerShortcut(context.Context, string, string, *bool, string) (*contract.RepoExplorerShortcutStatus, error)
}

func (adapter repositoryRelocateAdapter) ExplorerShortcut(ctx context.Context, serverID, repoID string, visible *bool, wcID string) (actions.ExplorerShortcutPreference, error) {
	client, ok := adapter.client.(explorerShortcutClient)
	if !ok {
		return actions.ExplorerShortcutPreference{}, errors.New("Explorer shortcut setting is unavailable")
	}
	result, err := client.RepoExplorerShortcut(ctx, serverID, repoID, visible, wcID)
	if err != nil {
		return actions.ExplorerShortcutPreference{}, err
	}
	if result == nil || result.ServerID != serverID || result.RepoID != repoID || result.WCID == "" || (visible != nil && result.WCID != wcID) {
		return actions.ExplorerShortcutPreference{}, errors.New("Explorer shortcut response scope mismatch")
	}
	return actions.ExplorerShortcutPreference{Visible: result.Visible, WCID: result.WCID}, nil
}
