package main

import (
	"context"
	"errors"

	"filees/internal/gui/platform"
	"filees/pkg/localpin"
)

// maxLaunchPinAttempts bounds how many wrong entries requireLocalPinAtLaunch
// tolerates in one process launch before refusing to start the GUI at all -
// separate from (and much smaller than) localpin.Store's own permanent
// per-PIN lockout budget, which persists across launches.
const maxLaunchPinAttempts = 3

// requireLocalPinAtLaunch is the optional startup gate: if a local PIN is
// configured and RequireOnLaunch is set, the GUI refuses to proceed past
// this point (no tray, no daemon connection) until the correct PIN is
// entered. A nil store, an unconfigured PIN, or RequireOnLaunch()==false
// all mean the gate is a no-op - this feature is opt-in.
func requireLocalPinAtLaunch(ctx context.Context, prompter platform.Prompter, store *localpin.Store) error {
	if store == nil || prompter == nil {
		return nil
	}
	require, err := store.RequireOnLaunch()
	if err != nil || !require {
		return nil
	}
	for attempt := 0; attempt < maxLaunchPinAttempts; attempt++ {
		prompted, err := prompter.PromptText(ctx, platform.PromptTextRequest{Title: "FileES", Text: "Podaj PIN, aby uruchomić FileES:", Secret: true})
		if err != nil {
			return err
		}
		if prompted.Cancelled {
			return errors.New("uruchomienie przerwane: PIN wymagany")
		}
		pin := []byte(prompted.Value)
		ok, locked, err := store.Verify(pin)
		clear(pin)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if locked {
			return errors.New("PIN zablokowany po zbyt wielu błędnych próbach")
		}
	}
	return errors.New("przekroczono limit prób podania PIN-u")
}
