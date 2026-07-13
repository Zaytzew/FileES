// Package archtest enforces the GUI import boundary defined in gui-assumptions.md:
// internal/gui/ must not import engine packages (watcher, commit, client, ipcserver, errmap).
package archtest

import (
	"os/exec"
	"strings"
	"testing"
)

var forbiddenPkgs = []string{
	"filees/pkg/watcher",
	"filees/pkg/commit",
	"filees/pkg/client",
	"filees/pkg/ipcserver",
	"filees/pkg/errmap",
}

func TestGUIDoesNotImportEnginePackages(t *testing.T) {
	root := moduleRoot(t)

	// Collect the full transitive dependency set of all internal/gui packages.
	cmd := exec.Command("go", "list", "-f", `{{join .Deps "\n"}}`, "filees/internal/gui/...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}

	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		seen[strings.TrimSpace(line)] = true
	}

	for _, pkg := range forbiddenPkgs {
		if seen[pkg] {
			t.Errorf("internal/gui imports forbidden engine package: %s", pkg)
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	return strings.TrimSpace(string(out))
}
