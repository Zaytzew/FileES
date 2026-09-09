package ipcclient

import (
	"context"
	contract "filees/pkg/contract/v1"
)

func (c *Client) RepoIntentPlan(ctx context.Context, repoID string) (*contract.IntentPlan, error) {
	resp, err := c.do(ctx, contract.CmdRepoIntentPlan, repoID, struct{}{})
	if err != nil {
		return nil, err
	}
	var result contract.IntentPlan
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RepoIntentApply(ctx context.Context, repoID, planID, choice string) (*contract.IntentApplyResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoIntentApply, repoID, contract.IntentApplyPayload{PlanID: planID, Choice: choice})
	if err != nil {
		return nil, err
	}
	var result contract.IntentApplyResult
	return &result, contract.DecodeResult(resp.Result, &result)
}
