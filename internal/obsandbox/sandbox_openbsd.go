//go:build openbsd

package obsandbox

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Begin establishes the owning tool's bootstrap pledge. A later pledge may
// only remove the temporary unveil promise, never add capabilities.
func Begin(runtimePromises string) error {
	if runtimePromises == "" {
		return fmt.Errorf("bootstrap pledge: empty runtime promises")
	}
	if err := unix.PledgePromises(runtimePromises + " unveil"); err != nil {
		return fmt.Errorf("bootstrap pledge %q: %w", runtimePromises+" unveil", err)
	}
	return nil
}

func Apply(profile Profile) error {
	if err := Validate(profile); err != nil {
		return err
	}
	for _, path := range profile.Paths {
		if err := unix.Unveil(path.Name, path.Perms); err != nil {
			return fmt.Errorf("sandbox %s: unveil %s=%q perms=%s: %w", profile.Name, path.Label, path.Name, path.Perms, err)
		}
	}
	if err := unix.UnveilBlock(); err != nil {
		return fmt.Errorf("sandbox %s: lock unveil: %w", profile.Name, err)
	}
	// PledgePromises is deliberate. Pledge(promises, "") would install an
	// empty execpromises profile rather than leave it genuinely unspecified.
	if err := unix.PledgePromises(profile.Promises); err != nil {
		return fmt.Errorf("sandbox %s: runtime pledge %q: %w", profile.Name, profile.Promises, err)
	}
	return nil
}
