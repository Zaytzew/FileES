//go:build !windows || nocfapi

package main

import (
	"errors"
	"filees/pkg/localrepo"
)

func anchorMergeBoundRevision(localrepo.Record) (int64, error) {
	return 0, errors.New("virtual folder merge unavailable on this build")
}
