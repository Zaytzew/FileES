package svntool

import (
	"os"
	"strings"
	"testing"
)

// withSnapshot replaces the process-start snapshot for one case and restores
// it afterwards. The tests drive the snapshot directly rather than depending
// on what this host happens to have installed: a test of the "is the
// toolchain here" logic must not itself change behaviour with the toolchain,
// which is the very confusion this package exists to remove.
func withSnapshot(t *testing.T, started map[string]string, require bool) {
	t.Helper()
	prevStarted, prevRequire := atStart, requireOn
	atStart, requireOn = started, require
	t.Cleanup(func() { atStart, requireOn = prevStarted, prevRequire })
}

// withEmptyPath makes every live exec.LookPath fail without touching the
// snapshot, which is what a process looks like after unveil has hidden the
// toolchain from it.
func withEmptyPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", "")
}

func TestAbsentAtStartIsAnHonestSkip(t *testing.T) {
	withSnapshot(t, map[string]string{}, false)
	reason, mustFail := Check("svnadmin")
	if mustFail {
		t.Fatalf("a host that never had svnadmin must be allowed to skip, got mustFail with %q", reason)
	}
	if !strings.Contains(reason, "not installed") {
		t.Fatalf("skip reason should say the tool is not installed, got %q", reason)
	}
	if !strings.Contains(reason, SkipMarker) {
		t.Fatalf("skip reason must carry %s so a run can be audited, got %q", SkipMarker, reason)
	}
}

// The regression this package was written for: on OpenBSD a tool that unveil
// has hidden reports exactly like a tool that was never installed. Collapsing
// the two is how a pledge defect once left the remaining fixtures skipping
// quietly while the suite reported clean.
func TestToolThatVanishedMidProcessIsAFailure(t *testing.T) {
	withSnapshot(t, map[string]string{"svnadmin": "/usr/local/bin/svnadmin"}, false)
	withEmptyPath(t)

	reason, mustFail := Check("svnadmin")
	if !mustFail {
		t.Fatal("a tool present at process start and unreachable now is a defect in this process, not a missing installation")
	}
	if strings.Contains(reason, "not installed") {
		t.Fatalf("the reason must not claim the tool is missing from the host, got %q", reason)
	}
	if !strings.Contains(reason, "sandbox") {
		t.Fatalf("the reason should point at the likely cause so the next reader is not sent to check their PATH, got %q", reason)
	}
}

// A vanished tool outranks an absent one: reporting "not installed" for a run
// that also contains a sandbox defect would describe the wrong problem.
func TestVanishedIsReportedBeforeAbsent(t *testing.T) {
	withSnapshot(t, map[string]string{"svnadmin": "/usr/local/bin/svnadmin"}, false)
	withEmptyPath(t)

	reason, mustFail := Check("svnadmin", "svnlook")
	if !mustFail {
		t.Fatal("a mixed case must still fail")
	}
	if !strings.Contains(reason, "svnadmin") {
		t.Fatalf("the vanished tool must be named, got %q", reason)
	}
	if strings.Contains(reason, "svnlook") {
		t.Fatalf("the absent tool must not be blamed alongside it, got %q", reason)
	}
}

func TestRequireEnvTurnsSkipIntoFailure(t *testing.T) {
	withSnapshot(t, map[string]string{}, true)
	reason, mustFail := Check("svnadmin")
	if !mustFail {
		t.Fatal("with the require flag set, an absent toolchain must fail rather than skip")
	}
	if !strings.Contains(reason, RequireEnv) {
		t.Fatalf("the reason should name the flag that caused the failure, got %q", reason)
	}
}

func TestEverythingAvailableProceeds(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve a real executable for the fixture: %v", err)
	}
	withSnapshot(t, map[string]string{"svnadmin": path}, false)
	// PATH is left alone: LookPath resolves the absolute path directly.
	if reason, mustFail := Check("svnadmin"); reason != "" || mustFail {
		t.Fatalf("an available tool must not stop the caller, got %q mustFail=%v", reason, mustFail)
	}
}

// The snapshot has to be taken before any test body runs, because a test that
// sandboxes the process would otherwise poison it for everything after.
func TestSnapshotIsTakenAtInit(t *testing.T) {
	if atStart == nil {
		t.Fatal("init must populate the snapshot; resolving lazily would capture a post-sandbox view")
	}
}
