package reservationclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"filees/pkg/publicstate"
	"golang.org/x/crypto/ssh"
)

var ErrPublicLinksUnsupported = errors.New("server does not support differential public link state")

func (c *Client) FetchPublicLinks(ctx context.Context, req publicstate.Request) (publicstate.Response, error) {
	if err := req.Validate(); err != nil {
		return publicstate.Response{}, err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return publicstate.Response{}, err
	}
	if len(raw) > publicstate.MaxRequestBytes {
		return publicstate.Response{}, errors.New("public link request exceeds limit")
	}
	response, err := c.exchange(ctx, raw, publicstate.MaxResponseBytes)
	var exit *ssh.ExitError
	// Only the old request parser's explicit schema refusal negotiates legacy.
	// Network errors, authorization failures and malformed responses never do.
	if errors.As(err, &exit) && (strings.Contains(err.Error(), "reservation request schema mismatch") || strings.Contains(err.Error(), `json: unknown field "known"`)) {
		return publicstate.Response{}, ErrPublicLinksUnsupported
	}
	if err != nil {
		return publicstate.Response{}, err
	}
	return publicstate.ParseResponse(response)
}
