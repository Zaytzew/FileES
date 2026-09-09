//go:build windows

package onboarding

import "testing"

// TestOnboardingStoreNeedsAdvisoryLocks is this package's whole Windows surface.
//
// Every test here builds the on-disk store, and every store operation goes
// through withLock — which this platform does not provide (lock_other.go says
// so, and fileLocksSupported reports it). Thirty-four of forty tests failed
// with the product's own "unsupported on this platform", which is a true answer
// to a question that cannot be asked here.
//
// This is deliberately NOT labelled "server-side package". The client links
// this package and uses onboarding.DecodeInvitation, a pure decode with no
// store and no lock. Calling the whole package server-side would be the
// coarse-grained mistake made in r1002 and corrected in r1003: the package is
// shared, and it is the store that is Unix-only.
//
// What that costs: DecodeInvitation has no Windows coverage. It parses an
// invitation string and touches nothing platform-specific, so the POSIX runs
// cover it — but if a test is ever written for it alone, it belongs outside
// this build tag.
func TestOnboardingStoreNeedsAdvisoryLocks(t *testing.T) {
	if fileLocksSupported() {
		t.Fatal("this file is built only where advisory locks are absent")
	}
	t.Skip("onboarding store requires advisory file locks; verified where they exist")
}
