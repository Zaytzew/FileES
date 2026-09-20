package ipcclient

import (
	"context"
	contract "filees/pkg/contract/v1"
)

func (c *Client) SyncPause(ctx context.Context, paused bool) error {
	_, err := c.do(ctx, contract.CmdSyncPause, "", contract.SyncPausePayload{Paused: paused})
	return err
}
func (c *Client) ShoutDraft(ctx context.Context, repo, token, action string) (bool, error) {
	resp, err := c.do(ctx, contract.CmdShoutDraft, repo, contract.ShoutDraftPayload{Token: token, Action: action})
	if err != nil {
		return false, err
	}
	var r contract.ShoutDraftResult
	err = contract.DecodeResult(resp.Result, &r)
	return r.Ready, err
}
