package main

import (
	"fmt"

	"filees/pkg/clientprofile"
	"filees/pkg/config"
	"filees/pkg/detachment"
)

// forgetDetachedProfiles closes the restart seam of revocation. The profile
// directory is the durable membership capability; a current detachment and a
// remaining profile are contradictory state, so detachment wins and the stale
// credentials are removed before any subsystem receives the profile list.
func forgetDetachedProfiles(root string, profiles []clientprofile.Profile, store *detachment.Store) ([]clientprofile.Profile, []error) {
	if store == nil {
		return profiles, nil
	}
	active := make([]clientprofile.Profile, 0, len(profiles))
	var failures []error
	for _, profile := range profiles {
		if !store.Current(profile.ServerID) {
			active = append(active, profile)
			continue
		}
		if err := clientprofile.Remove(root, profile.ServerID); err != nil {
			failures = append(failures, fmt.Errorf("forget detached server %s: %w", profile.ServerID, err))
		}
	}
	return active, failures
}

// withoutDetachedClientView disables the legacy config.json projection lane
// without disturbing the independent desktop update configuration.
func withoutDetachedClientView(view config.ClientView, store *detachment.Store) config.ClientView {
	if store == nil || !view.Configured || !store.Current(view.ServerID) {
		return view
	}
	view.ServerID = ""
	view.DisplayName = ""
	view.ClientRole = ""
	view.IdentityFile = ""
	view.KnownHosts = ""
	view.Projection = nil
	view.Configured = false
	return view
}

// serverMayStart gates repository runtimes that have durable local records but
// no longer have an active relationship with their server.
func serverMayStart(serverID string, store *detachment.Store) bool {
	return store == nil || !store.Current(serverID)
}
