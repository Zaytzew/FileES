package ipcclient

import (
	"context"

	contract "filees/pkg/contract/v1"
)

func (c *Client) RepoCommitRecoveryPlan(ctx context.Context, repoID string) (*contract.CommitRecoveryPlan, error) {
	resp, err := c.do(ctx, contract.CmdRepoCommitRecoveryPlan, repoID, struct{}{})
	if err != nil {
		return nil, err
	}
	var result contract.CommitRecoveryPlan
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RepoCommitRecoveryApply(ctx context.Context, repoID, planID, choice string) (*contract.CommitRecoveryApplyResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoCommitRecoveryApply, repoID, contract.CommitRecoveryApplyPayload{PlanID: planID, Choice: choice})
	if err != nil {
		return nil, err
	}
	var result contract.CommitRecoveryApplyResult
	return &result, contract.DecodeResult(resp.Result, &result)
}
