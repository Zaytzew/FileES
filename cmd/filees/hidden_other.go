//go:build !windows

package main

// hideFileesDir is a no-op outside Windows: the leading dot in ".filees"
// already makes it hidden by convention on every other platform.
func hideFileesDir(path string) error {
	return nil
}
