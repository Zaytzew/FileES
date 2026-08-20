package repoworker

import "errors"

type OwnershipCorrection struct {
	Inspected int `json:"inspected"`
	Corrected int `json:"corrected"`
}

func CorrectServiceWorkingCopyOwnership(string, string) (OwnershipCorrection, error) {
	return OwnershipCorrection{}, errors.New("service working-copy ownership correction is unavailable on Windows")
}
