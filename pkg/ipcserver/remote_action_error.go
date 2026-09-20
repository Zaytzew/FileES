package ipcserver

import (
	"context"
	"errors"
	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"filees/pkg/controlclient"
	"net"
)

// A transport failure cannot prove whether a destructive request was accepted.
// Keep the cause, but do not call an authentication/protocol failure offline.
func remoteActionError(id, code, key string, err error) contract.Response {
	var network *net.OpError
	var dns *net.DNSError
	if !errors.Is(err, controlclient.ErrIdentityRefused) && (errors.As(err, &network) || errors.As(err, &dns) || errors.Is(err, context.DeadlineExceeded) || client.IsNetworkError(err)) {
		key = "server.action_unreachable"
		code = "SERVER-1004"
	}
	return contract.ErrResponse(id, code, "ERROR", "REQUIRE_ACTION", key, map[string]string{"detail": err.Error()})
}
