//go:build !windows

package main

import "filees/internal/gui/actions"

// Virtual folders exist only in the Windows client.
func newAnchorRepairer() actions.AnchorRepairer { return nil }
