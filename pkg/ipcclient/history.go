package ipcclient

import (
	"context"

	contract "filees/pkg/contract/v1"
)

// Wehikuł czasu (concepts/REPOSITORY_HISTORY_CONCEPT.md §6). The daemon owns
// every decision - owner gate, paging, names, budgets - so these are plain
// round trips; a refusal arrives as the daemon's errcat key.

func (c *Client) HistoryResolve(ctx context.Context, payload contract.RepoHistoryResolvePayload) (*contract.RepoHistorySnapshot, error) {
	resp, err := c.do(ctx, contract.CmdRepoHistoryResolve, payload.RepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoHistorySnapshot
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) HistoryCommits(ctx context.Context, payload contract.RepoHistoryCommitsPayload) (*contract.RepoHistoryCommitsResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoHistoryCommits, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoHistoryCommitsResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) HistoryChanges(ctx context.Context, payload contract.RepoHistoryChangesPayload) (*contract.RepoHistoryChangesResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoHistoryChanges, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoHistoryChangesResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) HistoryList(ctx context.Context, payload contract.RepoHistoryListPayload) (*contract.RepoHistoryListResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoHistoryList, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoHistoryListResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) HistoryDensity(ctx context.Context, payload contract.RepoHistoryDensityPayload) (*contract.RepoHistoryDensityResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoHistoryDensity, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoHistoryDensityResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) HistoryFetch(ctx context.Context, payload contract.RepoHistoryFetchPayload) (*contract.RepoHistoryOperation, error) {
	return c.historyOperation(ctx, contract.CmdRepoHistoryFetch, payload)
}

func (c *Client) HistoryOperation(ctx context.Context, operationID string) (*contract.RepoHistoryOperation, error) {
	return c.historyOperation(ctx, contract.CmdRepoHistoryOperation, contract.RepoHistoryOperationPayload{OperationID: operationID})
}

func (c *Client) HistoryConfirm(ctx context.Context, operationID string) (*contract.RepoHistoryOperation, error) {
	return c.historyOperation(ctx, contract.CmdRepoHistoryConfirm, contract.RepoHistoryOperationPayload{OperationID: operationID})
}

func (c *Client) HistoryCancel(ctx context.Context, operationID string) (*contract.RepoHistoryOperation, error) {
	return c.historyOperation(ctx, contract.CmdRepoHistoryCancel, contract.RepoHistoryOperationPayload{OperationID: operationID})
}

func (c *Client) historyOperation(ctx context.Context, command string, payload any) (*contract.RepoHistoryOperation, error) {
	resp, err := c.do(ctx, command, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoHistoryOperation
	return &result, contract.DecodeResult(resp.Result, &result)
}
