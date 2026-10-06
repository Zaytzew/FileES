//go:build !openbsd

package main

import "filees/pkg/clientprofile"

func lockWhaleSandbox(whaleFlags, clientprofile.Profile) error { return nil }
