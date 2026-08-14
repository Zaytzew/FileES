package deploy

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PendingInvitationActivations returns accepted invitation attempts that do
// not yet have a published local client profile. It deliberately exposes no
// invitation token, OTP, private key, or reconnect material.
func PendingInvitationActivations(baseRoot string) ([]ServerProfile, error) {
	baseRoot = filepath.Clean(baseRoot)
	if !filepath.IsAbs(baseRoot) {
		return nil, errors.New("activation state root must be absolute")
	}
	entries, err := os.ReadDir(baseRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	profiles := make([]ServerProfile, 0)
	for _, entry := range entries {
		if !entry.IsDir() || strings.TrimSpace(entry.Name()) == "" {
			continue
		}
		root := filepath.Join(baseRoot, entry.Name())
		if _, err := os.Stat(filepath.Join(root, "client-profile.json")); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		passport, err := loadOnboardPassport(filepath.Join(root, "onboard.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		// Accepted passports written by older clients have no token hash, but
		// are still safe to resume: the server profile and proposed realm are
		// already bound and no invitation secret is exposed by this API.
		if passport.State != passportAccepted || passport.ProposedRealmID == "" {
			continue
		}
		profile := ServerProfile{ID: passport.ServerID, Address: passport.ServerAddress, KnownHostsPath: passport.KnownHostsPath}
		if err := profile.validate(); err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })
	return profiles, nil
}
