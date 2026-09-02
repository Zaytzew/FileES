//go:build openbsd

package svntool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/obsandbox"
)

// This is the case the package was written for, and it can only be observed on
// the real platform: unveil makes an installed binary unreachable, so
// exec.LookPath reports it exactly like a binary that was never there. The
// unit tests simulate that by emptying PATH, which proves the decision logic
// but not the premise. Here the premise is measured.
//
// It runs in a re-executed child because pledge and unveil are irreversible
// for the process that applies them: doing this inline would poison every test
// that ran afterwards in the same binary, which is the very failure this
// package exists to surface.
func TestUnveiledToolIsAFailureNotASkipOnOpenBSD(t *testing.T) {
	const probe = "FILEES_SVNTOOL_PROBE"

	if os.Getenv(probe) == "unveil" {
		// The snapshot was taken by init, before this body ran and before the
		// sandbox below exists. That ordering is the whole mechanism.
		if _, ok := Lookup("svnadmin"); !ok {
			t.Skip("svnadmin absent at process start; nothing can vanish")
		}
		root := os.Getenv("FILEES_SVNTOOL_ROOT")
		if err := obsandbox.Begin("stdio rpath"); err != nil {
			t.Fatal(err)
		}
		if err := obsandbox.Apply(obsandbox.Profile{
			Name:     "probe/svntool",
			Promises: "stdio rpath",
			Paths:    []obsandbox.Path{{Label: "allowed", Name: root, Perms: "r"}},
		}); err != nil {
			t.Fatal(err)
		}

		// Premise: the binary is installed, and this process can no longer see it.
		if _, err := exec.LookPath("svnadmin"); err == nil {
			t.Fatal("unveil did not hide the toolchain, so this platform cannot exhibit the confusion this package resolves")
		}

		reason, mustFail := Check("svnadmin")
		if !mustFail {
			t.Fatalf("a tool hidden by unveil must fail rather than skip, got skip with %q", reason)
		}
		if strings.Contains(reason, "not installed") {
			t.Fatalf("the reason must not blame the installation, got %q", reason)
		}
		if !strings.Contains(reason, "sandbox") {
			t.Fatalf("the reason should point at the sandbox, got %q", reason)
		}
		return
	}

	if _, ok := Lookup("svnadmin"); !ok {
		t.Skip(SkipMarker + " svnadmin not installed on this host")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "allowed"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestUnveiledToolIsAFailureNotASkipOnOpenBSD$", "-test.v")
	command.Env = append(os.Environ(), probe+"=unveil", "FILEES_SVNTOOL_ROOT="+root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("unveil probe: %v: %s", err, output)
	}
}
