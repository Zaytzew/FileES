//go:build windows

package activation

import (
	"context"
	"errors"
)

func (m *Manager) WithRealmSessionsStopped(context.Context, string, func() error) error {
	return errors.New("grant session barrier is unsupported on windows")
}
