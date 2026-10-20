//go:build !windows && !nocfapi

package main

import "os"

// Non-Windows builds have no CFAPI provider. This keeps isolated bridge tests
// portable; it is not a Unix file-custody claim.
func anchorOpenMaterialization(path string) (*os.File, error) { return os.Open(path) }
