//go:build windows && !nocfapi

package main

import "filees/internal/anchorrepair"

// A variable so tests can stand in for the file system.
var anchorDamageScan = func(root string) ([]anchorrepair.Damaged, error) {
	return anchorrepair.Scan(anchorrepair.Windows{}, root)
}
