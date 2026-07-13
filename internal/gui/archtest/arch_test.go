// Package archtest enforces the GUI import boundary defined in gui-assumptions.md:
// internal/gui/ must not import engine packages (watcher, commit, client, ipcserver, errmap).
package archtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var forbiddenPkgs = []string{
	"filees/pkg/watcher",
	"filees/pkg/commit",
	"filees/pkg/client",
	"filees/pkg/ipcserver",
	"filees/pkg/errmap",
	"filees/pkg/ipcclient",
}

func TestGUIDoesNotImportEnginePackages(t *testing.T) {
	root := moduleRoot(t)

	// Collect the full transitive dependency set of all GUI packages. The
	// composition root is included as soon as cmd/filees-gui exists.
	patterns := []string{"filees/internal/gui/..."}
	if _, err := os.Stat(filepath.Join(root, "cmd", "filees-gui")); err == nil {
		patterns = append(patterns, "filees/cmd/filees-gui")
	}
	args := append([]string{"list", "-f", `{{join .Deps "\n"}}`}, patterns...)
	cmd := exec.Command("go", args...)
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

func TestTrayImportsOnlyPresentationBoundary(t *testing.T) {
	root := moduleRoot(t)
	cmd := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, "filees/internal/gui/tray")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list tray imports: %v", err)
	}

	forbidden := []string{
		"filees/pkg/contract/v1",
		"filees/pkg/ipcclient",
	}
	imports := "\n" + strings.TrimSpace(string(out)) + "\n"
	for _, pkg := range forbidden {
		if strings.Contains(imports, "\n"+pkg+"\n") {
			t.Errorf("internal/gui/tray directly imports boundary implementation: %s", pkg)
		}
	}
}

func TestPlatformHasNoGUIOrDaemonDependencies(t *testing.T) {
	root := moduleRoot(t)
	cmd := exec.Command("go", "list", "-f", "{{range .Imports}}{{println .}}{{end}}", "filees/internal/gui/platform/...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list platform imports: %v", err)
	}

	forbidden := append([]string{
		"filees/internal/gui/app",
		"filees/internal/gui/tray",
		"filees/pkg/contract/v1",
		"filees/pkg/ipcclient",
	}, forbiddenPkgs...)
	imports := "\n" + strings.TrimSpace(string(out)) + "\n"
	for _, pkg := range forbidden {
		if strings.Contains(imports, "\n"+pkg+"\n") {
			t.Errorf("internal/gui/platform directly imports forbidden package: %s", pkg)
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
