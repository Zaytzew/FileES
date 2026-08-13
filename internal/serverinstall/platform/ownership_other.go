//go:build !linux && !openbsd

package platform

import (
	"fmt"
	"os"
)

func statOwnership(info os.FileInfo) (Ownership, error) {
	return Ownership{}, fmt.Errorf("file ownership is not supported on this platform")
}
