//go:build !windows || nocfapi

package main

import "testing"

func TestAnchorRepairUnavailableBuildRefusesEveryRequest(t *testing.T) {
	for _, args := range [][]string{nil, {"--root", t.TempDir()}, {"--root", t.TempDir(), "--reattach"}} {
		if got := cmdAnchorRepair(args); got != 1 {
			t.Fatalf("unavailable repair accepted %q: exit %d", args, got)
		}
	}
}
