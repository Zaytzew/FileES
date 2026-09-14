//go:build openbsd

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const identityHelperEnv = "FILEES_ADMIN_IDENTITY_TEST_HELPER"

func TestPrepareAdminIdentityDropsRootPermanently(t *testing.T) {
	if os.Getenv(identityHelperEnv) == "1" {
		if err := prepareAdminIdentity(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := unix.Setresuid(0, 0, 0); err == nil {
			fmt.Fprintln(os.Stderr, "root identity could be regained")
			os.Exit(3)
		}
		fmt.Printf("%d:%d", os.Geteuid(), os.Getegid())
		os.Exit(0)
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root on a real OpenBSD host")
	}
	target, err := user.Lookup(adminStateUser)
	if err != nil {
		t.Fatal(err)
	}
	wantUID, err := strconv.Atoi(target.Uid)
	if err != nil {
		t.Fatal(err)
	}
	wantGID, err := strconv.Atoi(target.Gid)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestPrepareAdminIdentityDropsRootPermanently$")
	cmd.Env = append(os.Environ(), identityHelperEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("identity helper: %v: %s", err, out)
	}
	if got, want := strings.TrimSpace(string(out)), fmt.Sprintf("%d:%d", wantUID, wantGID); got != want {
		t.Fatalf("effective identity=%q, want %q", got, want)
	}
}
