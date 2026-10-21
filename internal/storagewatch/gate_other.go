//go:build !linux && !openbsd

package storagewatch

import (
	"context"
	"errors"
)

func (g Gate) withLock(context.Context, bool, func() error) error {
	return errors.New("capacity writer barrier is unsupported on this server platform")
}

func (g Gate) withNamedLock(context.Context, string, bool, func() error) error {
	return errors.New("installation stop is unsupported on this server platform")
}
