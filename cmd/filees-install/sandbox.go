// sandbox.go applies the bootstrap pledge for the whole filees-install run.
// unveil is deliberately NOT called here — see internal/serverinstall/updater/sandbox.go.
// In short: which install targets need unveiling is not known until the
// manifest (or local state for rollback/purge) is resolved, and all path
// resolution must complete before the process's first unveil() call. So
// unveil happens all at once inside the updater's Check/Apply/Rollback/Purge
// methods, after they resolve the manifest or state.
package main

import (
	"filees/internal/obsandbox"
)

// initSandbox applies the initial pledge that covers the full run.
// proc+exec are needed throughout because system tasks (useradd, rcctl,
// cap_mkdb, ssh-keygen, etc.) are exec'd on first-install and purge, and
// the svn subprocess is exec'd for manifest and payload fetches.
func initSandbox() error {
	const runtimePromises = "stdio rpath wpath cpath fattr proc exec"
	return obsandbox.Begin(runtimePromises)
}
