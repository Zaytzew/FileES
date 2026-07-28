package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"filees/pkg/onboarding"

	"github.com/google/uuid"
)

// BeginInvitation imports the opaque invitation delivered by the server and
// starts its OTP flow. The SSH pin is persisted before the first connection;
// it is never learned implicitly from the network.
func BeginInvitation(ctx context.Context, baseRoot, wire string) (OnboardPassport, ServerProfile, error) {
	invitation, err := onboarding.DecodeInvitation(wire)
	if err != nil {
		return OnboardPassport{}, ServerProfile{}, err
	}
	profile := ServerProfile{ID: invitation.ServerID, Address: invitation.ServerAddress, KnownHostsPath: filepath.Join(filepath.Clean(baseRoot), invitation.ServerID, "known_hosts")}
	root, err := profileStateRoot(baseRoot, profile)
	if err != nil {
		return OnboardPassport{}, ServerProfile{}, err
	}
	knownHosts := filepath.Join(root, "known_hosts")
	want := invitation.KnownHost + "\n"
	if existing, readErr := os.ReadFile(knownHosts); readErr == nil {
		if string(existing) != want {
			return OnboardPassport{}, ServerProfile{}, errors.New("invitation conflicts with the pinned server host key")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return OnboardPassport{}, ServerProfile{}, readErr
	} else if err := writeBytesAtomic(knownHosts, []byte(want), 0o600); err != nil {
		return OnboardPassport{}, ServerProfile{}, err
	}

	passport, err := beginInvitation(ctx, root, profile, invitation.Token)
	return passport, profile, err
}

func beginInvitation(ctx context.Context, root string, profile ServerProfile, token string) (OnboardPassport, error) {
	if err := profile.validate(); err != nil {
		return OnboardPassport{}, err
	}
	if err := onboarding.ValidateInvitationToken(token); err != nil {
		return OnboardPassport{}, err
	}
	path := filepath.Join(filepath.Clean(root), "onboard.json")
	passport, err := loadOnboardPassport(path)
	if errors.Is(err, os.ErrNotExist) {
		passport = OnboardPassport{Schema: OnboardPassportSchema, State: passportPending, InvitationToken: token, ProposedRealmID: uuid.NewString(), OnboardingRequestID: uuid.NewString(), ServerID: profile.ID, ServerAddress: profile.Address, KnownHostsPath: filepath.Clean(profile.KnownHostsPath)}
		created, createErr := writeJSONExclusive(path, passport, 0o600)
		if createErr != nil {
			return OnboardPassport{}, createErr
		}
		if !created {
			passport, err = loadOnboardPassport(path)
		} else {
			err = nil
		}
	}
	if err != nil {
		return OnboardPassport{}, err
	}
	if err := passport.validate(); err != nil {
		return OnboardPassport{}, err
	}
	if passport.ServerID != profile.ID || passport.ServerAddress != profile.Address || passport.KnownHostsPath != filepath.Clean(profile.KnownHostsPath) || passport.ProposedRealmID == "" {
		return OnboardPassport{}, errors.New("onboard passport belongs to another invitation")
	}
	if passport.State == passportAccepted {
		return passport, nil
	}
	response, err := SubmitInvitation(ctx, profile, passport.InvitationToken, passport.ProposedRealmID, passport.OnboardingRequestID)
	if err != nil {
		return OnboardPassport{}, err
	}
	passport.State, passport.WorkerPublicKey, passport.RemotePort, passport.InvitationToken = passportAccepted, strings.TrimSpace(response.WorkerPublicKey), int(response.AssignedReversePort), ""
	if err := passport.validate(); err != nil {
		return OnboardPassport{}, err
	}
	if err := writeJSONAtomic(path, passport, 0o600); err != nil {
		return OnboardPassport{}, err
	}
	return passport, nil
}
