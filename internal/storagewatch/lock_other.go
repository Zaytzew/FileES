//go:build !linux && !openbsd

package storagewatch

import (
	"errors"
	"os"
)

func Lock(string) (*os.File, error) {
	return nil, errors.New("capacity monitor requires Linux or OpenBSD")
}
