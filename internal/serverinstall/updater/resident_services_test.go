package updater

import (
	"bytes"
	"strings"
	"testing"

	"filees/internal/serverinstall/manifest"
)

// The install replaces the binary and the running service keeps the image it
// started with, so these two are the only components still executing the
// previous release when apply reports success. On 2026-09-03 that produced the
// obvious wrong conclusion: the state emission in the same release worked
// immediately, the public link fixes appeared to do nothing, and the release
// looked broken when only a restart was missing.
func TestInstallSaysWhichServicesStillRunTheOldRelease(t *testing.T) {
	var out bytes.Buffer
	reportResidentServices(&out, []manifest.File{
		{Target: "/usr/local/libexec/filees/filees-links"},
		{Target: "/usr/local/libexec/filees/filees-public-authority"},
		{Target: "/usr/local/libexec/filees/filees-worker"},
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
	services := residentServicesNeedingRestart([]manifest.File{
		{Target: "/usr/local/libexec/filees/filees-links"},
		{Target: "/usr/local/libexec/filees/filees-public-authority"},
	})
	if len(services) != 2 || services[0] != "filees_public_authority" || services[1] != "filees_links" {
		t.Fatalf("order = %v; the authority has to come up first", services)
	}
}

// An install that touched nothing resident says nothing. A line printed after
// every upgrade regardless would be ignored by the time it mattered.
func TestNothingIsSaidWhenNoServiceIsAffected(t *testing.T) {
	var out bytes.Buffer
	reportResidentServices(&out, []manifest.File{
		{Target: "/usr/local/libexec/filees/filees-worker"},
		{Target: "/usr/local/libexec/filees/filees-serving-state"},
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

	services := residentServicesNeedingRestart([]manifest.File{{Target: "/usr/local/libexec/filees/filees-future"}})
	if len(services) != 1 || services[0] != "filees_future" {
		t.Fatalf("services = %v", services)
	}
}
