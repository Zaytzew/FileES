package ipcclient

import (
	"context"
	contract "filees/pkg/contract/v1"
)

func (c *Client) AnchorConsent(ctx context.Context, payload contract.RepoAnchorConsentPayload) (*contract.RepoAnchorConsentResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoAnchorConsent, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoAnchorConsentResult
	return &result, contract.DecodeResult(resp.Result, &result)
}
