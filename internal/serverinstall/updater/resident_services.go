package updater

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// residentServices maps a shipped binary to the rc.d service that runs it.
//
// Most FileES executables are exec'd per request by a dispatcher or by sshd,
// so a replaced binary is in use from the very next request and an install
// needs no follow-up. These two are different: they are started once by rc.d
// and keep running the image they were started with, so after an install they
// are the only components still executing the previous release.
//
// That asymmetry produced a false conclusion the first time it was met on
// 2026-09-03: filees-serving-state picked up its change immediately while the
// public link fixes in the same release appeared to have done nothing, because
// filees-links had not been restarted. An installer that knows it replaced
// these files and says nothing leaves the operator to discover that, and the
// obvious reading of the evidence is that the release is broken.
var residentServices = map[string]string{
	"filees-links":            "filees_links",
	"filees-public-authority": "filees_public_authority",
}

// restartOrder lists services parents-first. filees-links talks to the
// authority over a backchannel socket, so restarting the authority second
// leaves the frontend briefly pointed at nothing.
var restartOrder = []string{"filees_public_authority", "filees_links"}

// residentServicesNeedingRestart returns the rc.d services whose binary this
// install actually replaced, in the order they should be restarted.
//
// It takes the plan rather than the manifest deliberately. Both resident
// binaries appear in every manifest, so reporting from there would print the
// same three lines after every upgrade including the ones that changed neither
// - and a notice that appears unconditionally is read as decoration by the time
// it carries something. Only ADD and UPDATE mean the running image is now stale;
// UNCHANGED leaves it current, and METADATA rewrites ownership or mode without
// touching the bytes the process is executing.
func residentServicesNeedingRestart(files []FilePlan) []string {
	needed := map[string]bool{}
	for _, file := range files {
		if file.Action != "ADD" && file.Action != "UPDATE" {
			continue
		}
		if service, ok := residentServices[path.Base(strings.ReplaceAll(file.Target, `\`, "/"))]; ok {
			needed[service] = true
		}
	}
	ordered := make([]string, 0, len(needed))
	for _, service := range restartOrder {
		if needed[service] {
			ordered = append(ordered, service)
			delete(needed, service)
		}
	}
	// Anything added to the map later without a place in restartOrder still
	// gets reported, so a new resident service cannot be silently dropped.
	rest := make([]string, 0, len(needed))
	for service := range needed {
		rest = append(rest, service)
	}
	sort.Strings(rest)
	return append(ordered, rest...)
}

// reportResidentServices tells the operator what is still running the previous
// release and exactly how to finish.
//
// It says rather than does. Restarting on the installer's own initiative would
// cut whatever those services are serving at that moment - a public share
// download in flight, an upload being received - and an install is not the
// place to decide that for someone. The command is printed in full so finishing
// costs a paste rather than a search through documentation.
func reportResidentServices(out io.Writer, files []FilePlan) {
	services := residentServicesNeedingRestart(files)
	if len(services) == 0 || out == nil {
		return
	}
	fmt.Fprintf(out, "[UP] resident services still running the previous release: %s\n", strings.Join(services, " "))
	fmt.Fprintf(out, "[UP] they keep the image they were started with; restart to finish the upgrade:\n")
	fmt.Fprintf(out, "[UP]     rcctl restart %s\n", strings.Join(services, " "))
}
