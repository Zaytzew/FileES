//go:build !linux && !windows

package deploy

import (
	"context"
	"errors"
)

func RunOpenSSHTunnel(context.Context, TunnelSpec, []byte) error {
	return errors.New("push bootstrap tunnel is currently supported only on Linux")
}

func RunOpenSSHReconnectTunnel(context.Context, TunnelSpec, string) error {
	return errors.New("push reconnect tunnel is currently supported only on Linux")
}

func AskpassConfigured() bool { return false }

func RunAskpass() error {
	return errors.New("push bootstrap askpass is currently supported only on Linux")
}
