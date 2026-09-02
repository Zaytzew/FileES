package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filees/pkg/clientprofile"

	"filees/pkg/onboarding"
	"filees/pkg/privatefile"

	"github.com/google/uuid"
)

type invitationSubmitter func(context.Context, ServerProfile, string, string, string) (onboarding.OnboardResponse, error)

// BeginInvitation imports the opaque invitation delivered by the server and
// starts its OTP flow. The SSH pin is persisted before the first connection;
// it is never learned implicitly from the network.
func BeginInvitation(ctx context.Context, baseRoot, wire string) (OnboardPassport, ServerProfile, error) {
	invitation, err := onboarding.DecodeInvitation(wire)
	if err != nil {
		return OnboardPassport{}, ServerProfile{}, err
	}
	// Through ServerDir, not a raw join: profileStateRoot below creates the
	// directory under the encoded name, and building this path from the ID
	// itself pointed at a directory that could not exist.
	serverDir, err := clientprofile.ServerDir(baseRoot, invitation.ServerID)
	if err != nil {
		return OnboardPassport{}, ServerProfile{}, err
	}
	profile := ServerProfile{ID: invitation.ServerID, Address: invitation.ServerAddress, KnownHostsPath: filepath.Join(serverDir, "known_hosts")}
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
	return beginInvitationWithSubmit(ctx, root, profile, token, SubmitInvitation)
}

func beginInvitationWithSubmit(ctx context.Context, root string, profile ServerProfile, token string, submit invitationSubmitter) (OnboardPassport, error) {
	if err := profile.validate(); err != nil {
		return OnboardPassport{}, err
	}
	if err := onboarding.ValidateInvitationToken(token); err != nil {
		return OnboardPassport{}, err
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return OnboardPassport{}, errors.New("invitation state root must be absolute")
	}
	if err := privatefile.EnsureDir(root); err != nil {
		return OnboardPassport{}, err
	}
	path := filepath.Join(root, "onboard.json")
	passport, err := loadOnboardPassport(path)
	if errors.Is(err, os.ErrNotExist) {
		passport = newInvitationPassport(profile, token)
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
	incomingHash := invitationTokenHash(token)
	storedHash := passport.InvitationTokenHash
	if storedHash == "" && passport.InvitationToken != "" {
		storedHash = invitationTokenHash(passport.InvitationToken)
	}
	if storedHash != incomingHash {
		if err := resetSupersededInvitationAttempt(filepath.Clean(root)); err != nil {
			return OnboardPassport{}, err
		}
		passport = newInvitationPassport(profile, token)
		if err := writeJSONAtomic(path, passport, 0o600); err != nil {
			return OnboardPassport{}, fmt.Errorf("replace superseded onboard passport: %w", err)
		}
	} else if passport.InvitationTokenHash == "" {
		// One-time migration for pending passports written before the hash was
		// introduced. Accepted legacy passports deliberately fall into the
		// supersede branch because their consumed token can no longer be proven
		// equal to the invitation the user just supplied.
		passport.InvitationTokenHash = storedHash
		if err := writeJSONAtomic(path, passport, 0o600); err != nil {
			return OnboardPassport{}, err
		}
	}
	if passport.State == passportAccepted {
		return passport, nil
	}
	response, err := submit(ctx, profile, passport.InvitationToken, passport.ProposedRealmID, passport.OnboardingRequestID)
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

func newInvitationPassport(profile ServerProfile, token string) OnboardPassport {
	return OnboardPassport{
		Schema: OnboardPassportSchema, State: passportPending,
		InvitationToken: token, InvitationTokenHash: invitationTokenHash(token),
		ProposedRealmID: uuid.NewString(), OnboardingRequestID: uuid.NewString(),
		ServerID: profile.ID, ServerAddress: profile.Address, KnownHostsPath: filepath.Clean(profile.KnownHostsPath),
	}
}

// resetSupersededInvitationAttempt removes only ephemeral state belonging to
// an activation that never published a client profile. Supplying a different,
// server-signed invitation is the explicit user authority to abandon that
// attempt. An existing client-profile.json is a hard boundary: an activated
// installation must be detached/revoked, never overwritten by onboarding.
func resetSupersededInvitationAttempt(root string) error {
	if _, err := os.Stat(filepath.Join(root, "client-profile.json")); err == nil {
		return errors.New("server is already activated; detach it before using another invitation")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	deployRequestID := ""
	if raw, err := os.ReadFile(filepath.Join(root, "activation.json")); err == nil {
		var session ActivationSession
		if json.Unmarshal(raw, &session) == nil {
			if _, parseErr := uuid.Parse(session.DeployRequestID); parseErr == nil {
				deployRequestID = session.DeployRequestID
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, path := range []string{
		filepath.Join(root, "identity"),
		filepath.Join(root, "activation.json"),
		filepath.Join(root, "helper_host_ed25519"),
	} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	if deployRequestID != "" {
		if err := os.RemoveAll(filepath.Join(root, deployRequestID)); err != nil {
			return err
		}
	}
	return syncDir(root)
}
