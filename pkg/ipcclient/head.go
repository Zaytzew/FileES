package ipcclient

import (
	"context"

	contract "filees/pkg/contract/v1"
)

// Head browser for an unattached or sparse repository
// (implementation notes (not distributed)).

func (c *Client) HeadList(ctx context.Context, payload contract.RepoHeadListPayload) (*contract.RepoHeadListResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoHeadList, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoHeadListResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) HeadCat(ctx context.Context, payload contract.RepoHeadCatPayload) (*contract.RepoHeadCatResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoHeadCat, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoHeadCatResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

// AnchorCreate makes a folder the Explorer anchor of a repository without a
// copy here (capability repo.explorer_anchor).
func (c *Client) AnchorCreate(ctx context.Context, payload contract.RepoAnchorCreatePayload) (*contract.RepoHeadWriteResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoAnchorCreate, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoHeadWriteResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) HeadMaterialize(ctx context.Context, payload contract.RepoHeadMaterializePayload) (*contract.RepoHeadWriteResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoHeadMaterialize, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoHeadWriteResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) HeadFill(ctx context.Context, payload contract.RepoHeadFillPayload) (*contract.RepoHeadWriteResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoHeadFill, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoHeadWriteResult
	return &result, contract.DecodeResult(resp.Result, &result)
}
