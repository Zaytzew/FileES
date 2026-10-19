package ipcclient

import (
	"context"
	contract "filees/pkg/contract/v1"
)

func (c *Client) WCConversion(ctx context.Context, p contract.WCConversionPayload) (*contract.WCConversionStatus, error) {
	resp, err := c.do(ctx, contract.CmdWCConversion, "", p)
	if err != nil {
		return nil, err
	}
	var result contract.WCConversionStatus
	return &result, contract.DecodeResult(resp.Result, &result)
}
