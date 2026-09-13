//go:build !linux && !openbsd

package storage

import "errors"

func probeCapacity(string) (string, int64, error) {
	return "", 0, errors.New("server filesystem budgeting requires Linux or OpenBSD")
}
