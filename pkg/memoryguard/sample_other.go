//go:build !windows

package memoryguard

import "errors"

// No guessed physical/private-byte metric: unsupported hosts keep operating.
func ReadSample() (Sample, error) {
	return Sample{}, errors.New("memory sampler unavailable on this platform")
}
