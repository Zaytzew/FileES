//go:build !windows && !nocfapi

package main

import "filees/internal/anchorrepair"

// Virtual folders exist only on Windows.
var anchorDamageScan = func(string) ([]anchorrepair.Damaged, error) { return nil, nil }
