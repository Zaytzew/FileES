package servertool

import (
	"testing"

	"filees/internal/svntool"
)

// requireSVN resolves the named Subversion binaries for a fixture, or ends the
// test the way svntool decides: a host that never had the toolchain may skip,
// while a process that could see it at startup and cannot see it now must
// fail. The second case matters here in particular, because tests in this
// package apply pledge and unveil, and after that a bare exec.LookPath reports
// an installed binary as missing. The paths come from the snapshot taken
// before any test body ran, so they stay usable even where a live lookup no
// longer resolves.
func requireSVN(t *testing.T, names ...string) []string {
	t.Helper()
	if reason, mustFail := svntool.Check(names...); reason != "" {
		if mustFail {
			t.Fatal(reason)
		}
		t.Skip(reason)
	}
	paths := make([]string, len(names))
	for i, name := range names {
		path, ok := svntool.Lookup(name)
		if !ok {
			// Unreachable: Check has already accepted every name.
			t.Fatalf("svntool.Check accepted %q but the snapshot has no path for it", name)
		}
		paths[i] = path
	}
	return paths
}
