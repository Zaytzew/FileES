package servertool

import (
	"io"
	"path/filepath"

	"filees/pkg/repoworker"
)

// RunRealmQuotaMode serves the worker linked as a repository's pre-commit
// hook. It reads only the repositories root, so it narrows itself to that
// before looking at anything.
func RunRealmQuotaMode(program string, args []string, stderr io.Writer) (bool, int) {
	name := filepath.Base(program)
	if name != "pre-commit" && name != "pre-revprop-change" {
		return false, 0
	}
	if err := sandboxNarrow("stdio rpath"); err != nil {
		report(stderr, "realm quota sandbox", err)
		return true, ExitSoftware
	}
	if name == "pre-revprop-change" {
		return true, repoworker.RunArchiveRevpropGuard(args, stderr)
	}
	return true, repoworker.RunRealmQuotaGuard(args, stderr)
}
