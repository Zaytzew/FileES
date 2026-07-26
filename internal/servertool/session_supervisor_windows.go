//go:build windows

package servertool

import (
	"errors"
	"io"

	"filees/pkg/activation"
	"filees/pkg/serverconfig"
)

func RunClientSessionChild([]string, io.Writer) int { return ExitUnavailable }

func runSVNSessionSupervisor(serverconfig.Config, string, *activation.Manager, *activation.SessionLease, io.Reader, io.Writer, io.Writer) error {
	return errors.New("supervised SVN sessions are unsupported on windows")
}
