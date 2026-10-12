package ipcclient

import (
	"context"
	contract "filees/pkg/contract/v1"
)

func (c *Client) RepoExplorerShortcut(ctx context.Context, serverID, repoID string, visible *bool, wcID string) (*contract.RepoExplorerShortcutStatus, error) {
	resp, err := c.do(ctx, contract.CmdRepoExplorerShortcut, repoID, contract.RepoExplorerShortcutPayload{ServerID: serverID, RepoID: repoID, Visible: visible, WCID: wcID})
	if err != nil {
		return nil, err
	}
	var result contract.RepoExplorerShortcutStatus
	return &result, contract.DecodeResult(resp.Result, &result)
}
