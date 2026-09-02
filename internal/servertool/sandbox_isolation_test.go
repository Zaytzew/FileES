package servertool

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"filees/internal/obsandbox"
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

// permitRepeatedSandbox lets one test invoke a dispatcher more than once.
//
// Isolating per test is not the same as isolating per invocation. A test that
// runs `repo check-state` and then `repo prune` performs two dispatcher calls
// in one process, and the second bootstrap pledge fails with "operation not
// permitted" because the process is already pledged and a later pledge may
// only remove promises, never regain them.
//
// That failure is an artefact of the harness, not of the product: in
// production every invocation is its own process, forked by sshd or started by
// an operator, so the situation cannot arise. Splitting such tests into one
// invocation each would trade real coverage of a multi-step operator sequence
// for a limitation that only exists here.
//
// So the first application in the process stays real - the profile is still
// exercised, exactly as it would be in the single invocation production
// performs - and every later one is skipped. What is lost is only the repeated
// re-application, which asserts nothing that the first did not.
//
// Call it inside the isolated child, never in a shared process: it makes the
// sandbox lenient for this test, and the isolation is what keeps that leniency
// from reaching anything else.
func permitRepeatedSandbox(t *testing.T) {
	t.Helper()

	realBegin, realNarrow := sandboxBegin, sandboxNarrow
	realApply, realApplyForExec := sandboxApply, sandboxApplyForExec
	realPledgeForExec := sandboxPledgeForExec

	// An invocation is a Begin followed by exactly one Apply. Gating on "has
	// anything pledged yet" would suppress the Apply belonging to the FIRST
	// invocation, leaving the process holding only bootstrap promises and no
	// unveil table - which aborts on the first write. So the count is of
	// invocations, and a repeat is neutralised whole.
	begun := 0
	repeat := false

	sandboxBegin = func(promises string) error {
		if begun > 0 {
			repeat = true
			return nil
		}
		begun++
		repeat = false
		return realBegin(promises)
	}
	sandboxNarrow = func(promises string) error {
		if repeat {
			return nil
		}
		return realNarrow(promises)
	}
	sandboxApply = func(profile obsandbox.Profile) error {
		if repeat {
			return nil
		}
		return realApply(profile)
	}
	sandboxApplyForExec = func(profile obsandbox.Profile, execPromises string) error {
		if repeat {
			return nil
		}
		return realApplyForExec(profile, execPromises)
	}
	sandboxPledgeForExec = func(runtimePromises, execPromises string) error {
		if repeat {
			return nil
		}
		return realPledgeForExec(runtimePromises, execPromises)
	}

	t.Cleanup(func() {
		sandboxBegin, sandboxNarrow = realBegin, realNarrow
		sandboxApply, sandboxApplyForExec = realApply, realApplyForExec
		sandboxPledgeForExec = realPledgeForExec
	})
}

// sandboxTempDir returns a scratch directory for a test that sandboxes its own
// process.
//
// t.TempDir cannot be used by such a test. Its cleanup runs after the body,
// by which time pledge and unveil are already in force, so the removal fails
// with "unlinkat .../.svn/pristine: permission denied" and testing marks the
// test failed even though every assertion passed. Subversion makes that
// certain by storing pristine copies read-only, but the cause is not the
// permissions - it is that the process gave up the right to touch the path,
// deliberately and irreversibly, as part of what the test is checking.
//
// So removal is best effort here and never fails the test. The chmod pass
// exists for the ordinary case, where the process is still able to widen the
// tree before deleting it; when it is not, the directory simply outlives the
// run, which costs a temporary directory on a test host and nothing else.
func sandboxTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "filees-sandboxed-")
	if err != nil {
		t.Fatalf("scratch directory: %v", err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			} else {
				_ = os.Chmod(path, 0o600)
			}
			return nil
		})
		_ = os.RemoveAll(dir)
	})
	return dir
}
