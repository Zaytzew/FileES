package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filees/pkg/onboarding"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const OnboardPassportSchema = "filees.onboard-passport/v1"

const (
	passportPending  = "pending"
	passportAccepted = "accepted"
)

// OnboardPassport is the durable client-side half of the bootstrap exchange.
// The request ID is committed before SSH, so a lost response can be retried
// without consuming another ticket. The returned worker key is pinned before
// the caller may start a deploy helper or an OTP tunnel.
type OnboardPassport struct {
	Schema              string `json:"schema"`
	State               string `json:"state"`
	Email               string `json:"email,omitempty"`
	InvitationToken     string `json:"invitation_token,omitempty"`
	ProposedRealmID     string `json:"proposed_realm_id,omitempty"`
	OnboardingRequestID string `json:"onboarding_request_id"`
	WorkerPublicKey     string `json:"worker_public_key,omitempty"`
	RemotePort          int    `json:"remote_port,omitempty"`
	ServerID            string `json:"server_id"`
	ServerAddress       string `json:"server_address"`
	KnownHostsPath      string `json:"known_hosts_path"`
}

type onboardingSubmitter func(context.Context, ServerProfile, string, string) (onboarding.OnboardResponse, error)

// BeginOnboarding creates or resumes the installation-scoped bootstrap
// passport stored below root. A passport is bound to one delivery address;
// changing it requires an explicit removal by the client activation workflow.
func BeginOnboarding(ctx context.Context, root string, profile ServerProfile, email string) (OnboardPassport, error) {
	return beginOnboarding(ctx, root, profile, email, SubmitOnboarding)
}

func LoadOnboardPassport(root string, profile ServerProfile) (OnboardPassport, error) {
	root, err := profileStateRoot(root, profile)
	if err != nil {
		return OnboardPassport{}, err
	}
	passport, err := loadOnboardPassport(filepath.Join(root, "onboard.json"))
	if err != nil {
		return OnboardPassport{}, err
	}
	if passport.ServerID != profile.ID || passport.ServerAddress != profile.Address || passport.KnownHostsPath != filepath.Clean(profile.KnownHostsPath) {
		return OnboardPassport{}, errors.New("onboard passport belongs to another server profile")
	}
	return passport, nil
}

func beginOnboarding(ctx context.Context, root string, profile ServerProfile, email string, submit onboardingSubmitter) (OnboardPassport, error) {
	var err error
	root, err = profileStateRoot(root, profile)
	if err != nil {
		return OnboardPassport{}, err
	}
	canonical, err := onboarding.CanonicalEmail(email)
	if err != nil {
		return OnboardPassport{}, err
	}
	root = filepath.Clean(root)
	path := filepath.Join(root, "onboard.json")
	passport, err := loadOnboardPassport(path)
	if errors.Is(err, os.ErrNotExist) {
		passport = OnboardPassport{
			Schema: OnboardPassportSchema, State: passportPending, Email: canonical, OnboardingRequestID: uuid.NewString(),
			ServerID: profile.ID, ServerAddress: profile.Address, KnownHostsPath: filepath.Clean(profile.KnownHostsPath),
		}
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
	if passport.Email != canonical {
		return OnboardPassport{}, errors.New("onboard passport belongs to another delivery address")
	}
	if passport.ServerID != profile.ID || passport.ServerAddress != profile.Address || passport.KnownHostsPath != filepath.Clean(profile.KnownHostsPath) {
		return OnboardPassport{}, errors.New("onboard passport belongs to another server profile")
	}
	if passport.State == passportAccepted {
		return passport, nil
	}
	response, err := submit(ctx, profile, passport.Email, passport.OnboardingRequestID)
	if err != nil {
		return OnboardPassport{}, err
	}
	passport.State = passportAccepted
	passport.WorkerPublicKey = strings.TrimSpace(response.WorkerPublicKey)
	passport.RemotePort = int(response.AssignedReversePort)
	if err := passport.validate(); err != nil {
		return OnboardPassport{}, err
	}
	if err := writeJSONAtomic(path, passport, 0o600); err != nil {
		return OnboardPassport{}, fmt.Errorf("publish onboard passport: %w", err)
	}
	return passport, nil
}

func loadOnboardPassport(path string) (OnboardPassport, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return OnboardPassport{}, err
	}
	var passport OnboardPassport
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&passport); err != nil {
		return OnboardPassport{}, fmt.Errorf("decode onboard passport: %w", err)
	}
	if err := passport.validate(); err != nil {
		return OnboardPassport{}, err
	}
	return passport, nil
}

func (p OnboardPassport) validate() error {
	if p.Schema != OnboardPassportSchema {
		return errors.New("unsupported onboard passport schema")
	}
	invitationMode := p.Email == ""
	if invitationMode {
		if _, err := uuid.Parse(p.ProposedRealmID); err != nil {
			return errors.New("onboard passport proposed realm ID must be UUID")
		}
		if p.State == passportPending {
			if err := onboarding.ValidateInvitationToken(p.InvitationToken); err != nil {
				return err
			}
		} else if p.InvitationToken != "" {
			return errors.New("accepted invitation passport retains a capability")
		}
	} else if _, err := onboarding.CanonicalEmail(p.Email); err != nil {
		return err
	}
	if _, err := uuid.Parse(p.OnboardingRequestID); err != nil {
		return errors.New("onboard passport request ID must be UUID")
	}
	profile := ServerProfile{ID: p.ServerID, Address: p.ServerAddress, KnownHostsPath: p.KnownHostsPath}
	if err := profile.validate(); err != nil {
		return fmt.Errorf("onboard passport server profile: %w", err)
	}
	if p.State != passportPending && p.State != passportAccepted {
		return errors.New("onboard passport state is invalid")
	}
	if p.State == passportPending && p.WorkerPublicKey != "" {
		return errors.New("pending onboard passport contains a worker key")
	}
	if p.State == passportAccepted {
		if invitationMode && (p.RemotePort < 1 || p.RemotePort > 65535) {
			return errors.New("accepted onboard passport contains an invalid remote port")
		}
		key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(p.WorkerPublicKey)))
		if err != nil || key.Type() != ssh.KeyAlgoED25519 || comment != "" || len(options) != 0 || len(strings.TrimSpace(string(rest))) != 0 {
			return errors.New("accepted onboard passport contains an invalid worker key")
		}
	}
	return nil
}
