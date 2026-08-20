//go:build windows

package servertool

import (
	"errors"
	"io"

	"filees/pkg/activation"
	"filees/pkg/serverconfig"
)

func RunClientSessionChild([]string, io.Writer) int      { return ExitUnavailable }
func RunClientWhaleSessionChild([]string, io.Writer) int { return ExitUnavailable }

func runSVNSessionSupervisor(serverconfig.Config, string, *activation.Manager, *activation.SessionLease, io.Reader, io.Writer, io.Writer) (int, error) {
	return 0, errors.New("supervised SVN sessions are unsupported on windows")
}

func runWhaleSessionSupervisor(serverconfig.Config, string, *activation.Manager, *activation.SessionLease, io.Reader, io.Writer, io.Writer) (int, error) {
	return 0, errors.New("supervised Whale sessions are unsupported on windows")
}
