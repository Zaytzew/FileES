package updater

import (
	"bytes"
	"strings"
	"testing"
)

// The install replaces the binary and the running service keeps the image it
// started with, so these two are the only components still executing the
// previous release when apply reports success. On 2026-09-03 that produced the
// obvious wrong conclusion: the state emission in the same release worked
// immediately, the public link fixes appeared to do nothing, and the release
// looked broken when only a restart was missing.
func TestInstallSaysWhichServicesStillRunTheOldRelease(t *testing.T) {
	var out bytes.Buffer
	reportResidentServices(&out, []FilePlan{
		{Target: "/usr/local/libexec/filees/filees-links", Action: "UPDATE"},
		{Target: "/usr/local/libexec/filees/filees-public-authority", Action: "UPDATE"},
		{Target: "/usr/local/libexec/filees/filees-worker", Action: "UPDATE"},
	})

	report := out.String()
	if !strings.Contains(report, "filees_links") || !strings.Contains(report, "filees_public_authority") {
		t.Fatalf("both resident services must be named: %s", report)
	}
	if strings.Contains(report, "filees-worker") {
		t.Fatalf("a binary exec'd per request needs no restart and must not be listed: %s", report)
	}
	if !strings.Contains(report, "rcctl restart") {
		t.Fatalf("finishing must cost a paste, not a search through documentation: %s", report)
	}
}

// The frontend talks to the authority over a backchannel socket, so restarting
// the authority second leaves it pointed at nothing for the gap.
func TestTheAuthorityIsRestartedBeforeTheFrontend(t *testing.T) {
	services := residentServicesNeedingRestart([]FilePlan{
		{Target: "/usr/local/libexec/filees/filees-links", Action: "UPDATE"},
		{Target: "/usr/local/libexec/filees/filees-public-authority", Action: "ADD"},
	})
	if len(services) != 2 || services[0] != "filees_public_authority" || services[1] != "filees_links" {
		t.Fatalf("order = %v; the authority has to come up first", services)
	}
}

// An install that touched nothing resident says nothing. A line printed after
// every upgrade regardless would be ignored by the time it mattered.
func TestNothingIsSaidWhenNoServiceIsAffected(t *testing.T) {
	var out bytes.Buffer
	reportResidentServices(&out, []FilePlan{
		{Target: "/usr/local/libexec/filees/filees-worker", Action: "UPDATE"},
		{Target: "/usr/local/libexec/filees/filees-serving-state", Action: "UPDATE"},
	})
	if out.Len() != 0 {
		t.Fatalf("nothing resident was replaced, so there is nothing to say: %s", out.String())
	}
}

// A resident service added later must not fall out of the report because
// nobody remembered to place it in the restart order.
func TestAnUnorderedServiceIsStillReported(t *testing.T) {
	residentServices["filees-future"] = "filees_future"
	defer delete(residentServices, "filees-future")

	services := residentServicesNeedingRestart([]FilePlan{{Target: "/usr/local/libexec/filees/filees-future", Action: "UPDATE"}})
	if len(services) != 1 || services[0] != "filees_future" {
		t.Fatalf("services = %v", services)
	}
}

// Both resident binaries are in every manifest, so an upgrade that leaves them
// byte-identical must say nothing. This is the case that decides whether the
// notice is worth reading: printed after every install it becomes decoration,
// and the one time it carries something it has already been tuned out.
func TestAnUnchangedServiceIsNotReported(t *testing.T) {
	var out bytes.Buffer
	reportResidentServices(&out, []FilePlan{
		{Target: "/usr/local/libexec/filees/filees-links", Action: "UNCHANGED"},
		// METADATA rewrites ownership or mode; the running image keeps
		// executing the same bytes, so nothing needs restarting.
		{Target: "/usr/local/libexec/filees/filees-public-authority", Action: "METADATA"},
	})
	if out.Len() != 0 {
		t.Fatalf("neither binary changed, so there is nothing to restart: %s", out.String())
	}
}
