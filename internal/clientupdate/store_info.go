package clientupdate

import (
	"context"

	contract "filees/pkg/contract/v1"
)

// StoreInfo describes the running Store build without reading update state,
// resolving a remote channel or providing any file-installation operations.
// Windows/Store alone determines whether a newer package is available.
type StoreInfo struct {
	CurrentVersion string
}

func (info StoreInfo) Status(context.Context) (contract.UpdateStatus, error) {
	return contract.UpdateStatus{
		State: "store", Channel: "store",
		CurrentVersion: canonicalClientVersion(info.CurrentVersion),
	}, nil
}
