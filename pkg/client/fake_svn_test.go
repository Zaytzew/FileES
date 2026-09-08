package client

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The tests that pin how the daemon calls svn stand in for the binary by
// re-executing this test binary.
//
// They used to write a file named "svn" whose contents began "#!/bin/sh".
// Windows has neither shebangs nor extensionless executables, so exec reported
// "executable file not found in %PATH%" and three tests that exist to protect
// argument quoting could never run on the platform whose quoting rules are the
// hard ones. Re-executing the test binary is the portable form of the same
// trick and needs no shell.
const fakeSVNMode = "FILEES_TEST_FAKE_SVN"

func TestMain(m *testing.M) {
	switch os.Getenv(fakeSVNMode) {
	case "ssh-env":
		// What the daemon injected as SVN_SSH, verbatim.
		fmt.Print(os.Getenv("SVN_SSH"))
		os.Exit(0)
	case "args":
		// One argument per line, so a test can see exactly where "--" landed
		// and that nothing was re-split or re-quoted on the way.
		for _, arg := range os.Args[1:] {
			fmt.Println(arg)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeSVN returns a path to use as Options.SvnPath. Mode "ssh-env" prints the
// injected SVN_SSH; mode "args" prints one argument per line.
func fakeSVN(t *testing.T, mode string) string {
	t.Helper()
	t.Setenv(fakeSVNMode, mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	return exe
}

// testAbs builds an absolute path on every platform.
//
// Literals like "/run/filees/id_ed25519" are absolute on POSIX and not on
// Windows, where filepath.IsAbs wants a volume. The code under test asks
// IsAbs before it does anything - buildSSHCommand returns "" and relativize
// gives up - so a POSIX literal made these tests assert nothing here while
// still passing everywhere else.
func testAbs(t *testing.T, parts ...string) string {
	t.Helper()
	return filepath.Join(append([]string{t.TempDir()}, parts...)...)
}
