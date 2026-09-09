//go:build windows

package cache

import "testing"

// TestServerSideVerifiedNatively is this package's entire Windows surface.
//
// This is server code. It is verified natively on OpenBSD because only that is
// meaningful: a green Windows run has repeatedly looked fine and then failed on
// the server, for reasons Windows cannot express - POSIX file modes, advisory
// locks, pledge and unveil, an executable bit, "not writable by group or
// others". Windows answers those with ACLs, so it answers a different question
// and returns a reassuring result to one nobody asked.
//
// Coverage that can mislead is worse than none. The tests are therefore not
// built here, and this file exists so the package does not quietly disappear
// instead: a package that fails to build does not skip, it vanishes - which is
// how internal/servertool went unnoticed until r990.
func TestServerSideVerifiedNatively(t *testing.T) {
	t.Skip("server-side package: verified natively on OpenBSD, where its invariants exist")
}
