//go:build !unix

package repoworker

// sameVolume assumes one shared filesystem where the device cannot be
// compared, so the capacity check adds both needs instead of trusting either.
func sameVolume(a, b string) (bool, error) { return true, nil }
