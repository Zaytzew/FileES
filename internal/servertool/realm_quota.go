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
	if filepath.Base(program) != "pre-commit" {
		return false, 0
	}
	if err := sandboxNarrow("stdio rpath"); err != nil {
		report(stderr, "realm quota sandbox", err)
		return true, ExitSoftware
	}
	return true, repoworker.RunRealmQuotaGuard(args, stderr)
}
