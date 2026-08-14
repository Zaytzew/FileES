//go:build !unix && !windows

package updater

import (
	"fmt"
	"os"
	"runtime"
)

func lockRunFile(*os.File) error {
	return fmt.Errorf("installer locking is unsupported on %s", runtime.GOOS)
}
