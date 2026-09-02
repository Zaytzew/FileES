// Package svntool decides, for a test that needs the Subversion toolchain,
// whether an unavailable binary is a legitimate reason to skip or a failure
// that must be reported.
//
// The distinction is not cosmetic. Every call site used to be a bare
// exec.LookPath followed by t.Skip, which is correct only while "LookPath
// failed" means "this machine has no Subversion". On OpenBSD it can also mean
// the process has already applied unveil and can no longer see a binary that
// is installed and was visible a moment earlier — the exact case recorded in
// todo-control/handoff-queue on 2026-09-02, where a pledge failure surfaced
// as "svnserve unavailable" and the remaining fixtures skipped quietly.
//
// A suite that skips is a suite that says nothing, so the two causes must not
// share an outcome. This package resolves the toolchain once at process
// start, before any test body can sandbox the process, and treats a later
// disappearance as a failure rather than an absence.
package svntool

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

// RequireEnv forces every toolchain absence to be reported as a failure. Set
// it on release gates and on any host that is expected to have Subversion:
// without it a machine missing the toolchain reports a clean run, which is
// indistinguishable from a run that actually exercised these paths.
const RequireEnv = "FILEES_REQUIRE_SVN"

// SkipMarker prefixes every skip reason so a run can be audited without extra
// machinery: `go test -v ./... | grep FILEES-SKIP` counts what did not run.
const SkipMarker = "FILEES-SKIP"

// tools are the Subversion binaries the fixtures reach for. Resolving the
// closed set at init keeps the snapshot independent of call order.
var tools = []string{"svn", "svnadmin", "svnserve", "svnlook"}

var (
	once      sync.Once
	atStart   map[string]string
	requireOn bool
)

func snapshot() {
	once.Do(func() {
		atStart = make(map[string]string, len(tools))
		for _, name := range tools {
			if path, err := exec.LookPath(name); err == nil {
				atStart[name] = path
			}
		}
		requireOn = strings.TrimSpace(os.Getenv(RequireEnv)) != ""
	})
}

func init() { snapshot() }

// Lookup reports the path resolved at process start. Callers that only need
// the path of a tool they have already checked can use this directly.
func Lookup(name string) (string, bool) {
	snapshot()
	path, ok := atStart[name]
	return path, ok
}

// Check reports whether the named tools are usable by this test. An empty
// reason means every tool is available and the caller proceeds.
//
// A non-empty reason comes with mustFail, which distinguishes the two causes
// the old bare-LookPath pattern collapsed:
//
//   - mustFail false — the tool was already absent when the process started,
//     so this host genuinely has no Subversion and skipping is honest;
//   - mustFail true — the tool was present at process start and is not
//     reachable now, so something in this process removed it. On OpenBSD that
//     is unveil. Skipping there would hide a sandbox defect behind a message
//     about the environment.
//
// Setting FILEES_REQUIRE_SVN turns the first case into the second, which is
// how a release gate asserts that these fixtures really ran.
func Check(names ...string) (reason string, mustFail bool) {
	snapshot()
	var absent, vanished []string
	for _, name := range names {
		if _, started := atStart[name]; !started {
			absent = append(absent, name)
			continue
		}
		if _, err := exec.LookPath(name); err != nil {
			vanished = append(vanished, name)
		}
	}
	sort.Strings(absent)
	sort.Strings(vanished)

	// Reported first and unconditionally: a vanished tool is a defect in this
	// process, and saying "not installed" about it would be false.
	if len(vanished) > 0 {
		return fmt.Sprintf(
			"%s was visible when this process started and is unreachable now; "+
				"the sandbox applied by an earlier test in this process is the usual cause, "+
				"not a missing installation",
			strings.Join(vanished, ", "),
		), true
	}
	if len(absent) == 0 {
		return "", false
	}
	if requireOn {
		return fmt.Sprintf(
			"%s not installed, and %s requires the Subversion toolchain",
			strings.Join(absent, ", "), RequireEnv,
		), true
	}
	return fmt.Sprintf("%s %s not installed on this host", SkipMarker, strings.Join(absent, ", ")), false
}

// Missing reports the tools that were already absent at process start. It
// exists for a release gate that wants to refuse the whole run up front
// rather than one fixture at a time.
func Missing() []string {
	snapshot()
	var absent []string
	for _, name := range tools {
		if _, ok := atStart[name]; !ok {
			absent = append(absent, name)
		}
	}
	sort.Strings(absent)
	return absent
}
