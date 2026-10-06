//go:build !openbsd

package main

import "filees/pkg/clientprofile"

func lockDumpSandbox(dumpFlags, clientprofile.Profile) error { return nil }
