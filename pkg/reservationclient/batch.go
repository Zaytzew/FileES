package reservationclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	reservationv1 "filees/pkg/reservation/v1"
	"golang.org/x/crypto/ssh"
)

var ErrBatchUnsupported = errors.New("server does not support reservation batches")

func (c *Client) FetchBatch(ctx context.Context, req reservationv1.BatchRequest) (reservationv1.BatchResponse, error) {
	if err := req.Validate(); err != nil {
		return reservationv1.BatchResponse{}, err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return reservationv1.BatchResponse{}, err
	}
	if len(raw) > reservationv1.BatchMaxRequestBytes {
		return reservationv1.BatchResponse{}, errors.New("reservation batch request exceeds limit")
	}
	response, err := c.exchange(ctx, raw, reservationv1.BatchMaxResponseBytes)
	var exit *ssh.ExitError
	if ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && errors.As(err, &exit) && (strings.Contains(err.Error(), "reservation request schema mismatch") || strings.Contains(err.Error(), `json: unknown field "scope"`)) {
		return reservationv1.BatchResponse{}, ErrBatchUnsupported
	}
	if err != nil {
		return reservationv1.BatchResponse{}, err
	}
	return reservationv1.ParseBatchResponse(response)
}
