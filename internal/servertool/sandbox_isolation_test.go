package servertool

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// sandboxChildEnv marks the re-executed child so it runs the test body instead
// of delegating again.
const sandboxChildEnv = "FILEES_SANDBOXED_TEST_CHILD"

// isolateSandboxingTest re-executes one test in a child process on OpenBSD and
// reports whether the caller is the parent, in which case it must return at
// once because the child has already run the body.
//
// pledge and unveil narrow a process irreversibly, and `go test` runs every
// test of a package in one process. A test that drives a real dispatcher
// therefore does not only sandbox itself: it sandboxes everything scheduled
// after it, which then cannot see the Subversion toolchain and cannot undo the
// restriction either.
//
// Measured on OpenBSD 7.9 on 2026-09-02: one unisolated test in this package
// left twenty-one later tests unable to run. Before internal/svntool they
// reported that as "svnadmin unavailable" and skipped, so the package looked
// like 66 passed and 31 skipped when it was really 66 passed and 21 blinded.
//
// The pattern already existed here, hand-rolled four times under four
// different environment variable names, and applied to some tests that need it
// but not their immediate neighbours. This is that pattern with one name, so a
// new test can adopt it in one line rather than rediscovering the reason.
func isolateSandboxingTest(t *testing.T, name string) bool {
	t.Helper()
	// Elsewhere pledge is absent, the process is never narrowed, and paying for
	// a second process would buy nothing.
	if runtime.GOOS != "openbsd" {
		return false
	}
	if os.Getenv(sandboxChildEnv) != "" {
		return false
	}
	command := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.v")
	command.Env = append(os.Environ(), sandboxChildEnv+"=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("sandboxed child %s: %v: %s", name, err, output)
	}
	return true
}
