package packaging_test

import (
	"os"
	"strings"
	"testing"
)

// The desktop pair must stamp a version into both of its binaries.
//
// This is asserted rather than trusted because the failure it guards against
// already happened and lasted since July: the pair had no build script at all,
// every session built it by hand, none of them passed -X main.version, and the
// interface fell back to the VERSION file - a hand-edited constant. The badge
// therefore read 0.1.15 on every build, and on 2026-09-03 that made a fix which
// had reached production and never executed indistinguishable from one that had
// not shipped at all.
//
// One binary carrying the stamp is not enough. The daemon and the interface are
// updated together and the owner reads whichever he can reach; a pair where
// only one half can say what it is answers the question half the time.
func TestTheDesktopPairStampsAVersionIntoBothBinaries(t *testing.T) {
	raw, err := os.ReadFile("build-pair.sh")
	if err != nil {
		t.Fatalf("the desktop pair has no build script: %v", err)
	}
	script := string(raw)

	for _, pkg := range []string{"./cmd/filees", "./cmd/filees-gui-wails"} {
		var built string
		for _, line := range strings.Split(script, "\n") {
			if strings.Contains(line, "go build") && strings.HasSuffix(strings.TrimSpace(line), pkg) {
				built = line
				break
			}
		}
		if built == "" {
			t.Errorf("build-pair.sh does not build %s", pkg)
			continue
		}
		if !strings.Contains(built, "-X main.version=") {
			t.Errorf("build-pair.sh builds %s without stamping a version: %s", pkg, strings.TrimSpace(built))
		}
	}

	// The version has to move between builds, which the VERSION file alone
	// does not: it is a hand-edited constant and was touched twice in the
	// project's life. The working-copy revision is what every report, handoff
	// and message between sessions is numbered by, so it is the identifier
	// the badge should speak.
	if !strings.Contains(script, "svnversion") {
		t.Error("build-pair.sh derives no revision, so two different builds can still claim the same version")
	}
	// A build from a dirty tree is precisely the one nobody should mistake for
	// a release, so it has to be visible in the version itself.
	if !strings.Contains(script, `*M*`) {
		t.Error("build-pair.sh does not mark a build made from a modified working copy")
	}
}
