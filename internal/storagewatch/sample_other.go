//go:build !linux && !openbsd

package storagewatch

import "errors"

func (Native) Device(string) (uint64, error) {
	return 0, errors.New("capacity monitor requires Linux or OpenBSD")
}
func (Native) Sample(string) (Sample, error) {
	return Sample{}, errors.New("capacity monitor requires Linux or OpenBSD")
}
