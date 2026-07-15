//go:build !linux

package deploy

import (
	"context"
	"errors"
)

func RunOpenSSHTunnel(context.Context, TunnelSpec, []byte) error {
	return errors.New("push bootstrap tunnel is currently supported only on Linux")
}

func RunAskpass() error {
	return errors.New("push bootstrap askpass is currently supported only on Linux")
}
