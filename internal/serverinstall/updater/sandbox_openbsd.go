//go:build openbsd

package updater

import (
	"fmt"

	"golang.org/x/sys/unix"
)

var sandboxEnabled = func() bool { return true }

// applyUnveils resolves all paths to their nearest existing ancestors (one
// pass), then calls unveil(2) on each (second pass), then locks unveil.
// Two-pass order is load-bearing: unveil(2)
// hides everything not-yet-unveiled from its very first call, so any os.Stat
// used to decide "does this path exist" must complete before any unveil()
// call in this process.
//
// Only file-only runs call this — system runs skip unveil entirely; see the
// doc comment in sandbox.go for why exec'd system tools cannot live under a
// meaningful unveil profile.
func (r *Runner) applyUnveils(specs []unveilSpec) error {
	if !sandboxEnabled() {
		return nil
	}
	resolved := resolveAndMergeSpecs(specs)
	for _, s := range resolved {
		if err := unix.Unveil(s.Path, s.Perms); err != nil {
			return fmt.Errorf("security: unveil %s %s: %w", s.Label, s.Path, err)
		}
		if r.Config.Talkative {
			fmt.Fprintf(r.Out, "[SECURITY] unveiled %s=%q perms=%s\n", s.Label, s.Path, s.Perms)
		}
	}
	if err := unix.UnveilBlock(); err != nil {
		return fmt.Errorf("security: unveil lock: %w", err)
	}
	if r.Config.Talkative {
		fmt.Fprintln(r.Out, "[SECURITY] unveil locked")
	}
	return nil
}

// reducePledge drops to file promises: after unveil lock on file-only runs,
// or after the exec-needing system tasks complete on system runs.
func (r *Runner) reducePledge() error {
	if !sandboxEnabled() {
		return nil
	}
	if err := unix.PledgePromises(filePromises); err != nil {
		return fmt.Errorf("security: reduce pledge %q: %w", filePromises, err)
	}
	if r.Config.Talkative {
		fmt.Fprintf(r.Out, "[SECURITY] pledge reduced promises=%q\n", filePromises)
	}
	return nil
}
