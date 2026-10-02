//go:build windows

package activation

import (
	"context"
	"errors"
)

func (m *Manager) WithRealmSessionsStopped(context.Context, string, func() error) error {
	return errors.New("grant session barrier is unsupported on windows")
}

func (m *Manager) WithAllSessionsStopped(context.Context, func() error) error {
	return errors.New("global session barrier is unsupported on windows")
}
