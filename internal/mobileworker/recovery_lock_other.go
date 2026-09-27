//go:build !linux && !openbsd

package mobileworker

import (
	"errors"
	"os"
	"os/exec"
)

func inheritOperationLock(*exec.Cmd, *os.File) error {
	return errors.New("mobile worker operation fence requires Linux or OpenBSD")
}
