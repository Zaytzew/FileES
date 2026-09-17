package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/onboarding"
	"filees/pkg/privatefile"

	"github.com/google/uuid"
)

// The demo server is part of the client, not of an invitation: its address
// and host key are compiled in, so activating it needs no administrator and
// no capability beyond the OTP the server mails
// (implementation notes (not distributed), portion 3).
const (
	DemoServerID      = "demo"
	DemoServerAddress = "demo.filees.space:22"
	DemoKnownHost     = "demo.filees.space ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGJIjoVQnViMzEvlN6r8Le5/ZOMAdRH8KTqFITaq0Vma"
)

const DemoInstallationSchema = "filees.demo-installation/v1"

// DemoInstallation is installation-wide state beside the per-server
// directories, so neither detaching the demo server nor removing its profile
// resets it. The UID is what the demo server freezes for good; Used is the
// client's own record that the one demo of this installation is spent.
type DemoInstallation struct {
	Schema          string     `json:"schema"`
	InstallationUID string     `json:"installation_uid"`
	Used            bool       `json:"used,omitempty"`
	UsedReason      string     `json:"used_reason,omitempty"`
	UsedAt          *time.Time `json:"used_at,omitempty"`
}

var ErrDemoUsed = errors.New("the demo server has already been used from this installation")

// DemoRefusedError is the demo server's admission answer. RetryAfterMinutes
// is zero when the refusal is final.
type DemoRefusedError struct {
	Code              string
	RetryAfterMinutes int
}

func (e *DemoRefusedError) Error() string {
	if e.RetryAfterMinutes > 0 {
		return fmt.Sprintf("demo activation refused (%s); try again in %d min", e.Code, e.RetryAfterMinutes)
	}
	return fmt.Sprintf("demo activation refused (%s)", e.Code)
}

func demoInstallationPath(baseRoot string) string {
	return filepath.Join(filepath.Clean(baseRoot), "demo-installation.json")
}

// LoadDemoInstallation reads the installation's demo state; a missing file is
// an unused installation that has no UID yet.
func LoadDemoInstallation(baseRoot string) (DemoInstallation, error) {
	if !filepath.IsAbs(baseRoot) {
		return DemoInstallation{}, errors.New("demo state root must be absolute")
	}
	raw, err := os.ReadFile(demoInstallationPath(baseRoot))
	if errors.Is(err, os.ErrNotExist) {
		return DemoInstallation{Schema: DemoInstallationSchema}, nil
	}
	if err != nil {
		return DemoInstallation{}, err
	}
	var state DemoInstallation
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || state.Schema != DemoInstallationSchema {
		return DemoInstallation{}, errors.New("demo installation state is invalid")
	}
	if parsed, err := uuid.Parse(state.InstallationUID); err != nil || parsed.String() != state.InstallationUID {
		return DemoInstallation{}, errors.New("demo installation UID is invalid")
	}
	return state, nil
}

func ensureDemoInstallation(baseRoot string) (DemoInstallation, error) {
	state, err := LoadDemoInstallation(baseRoot)
	if err != nil || state.InstallationUID != "" {
		return state, err
	}
	if err := privatefile.EnsureDir(filepath.Clean(baseRoot)); err != nil {
		return DemoInstallation{}, err
	}
	state = DemoInstallation{Schema: DemoInstallationSchema, InstallationUID: uuid.NewString()}
	created, err := writeJSONExclusive(demoInstallationPath(baseRoot), state, 0o600)
	if err != nil {
		return DemoInstallation{}, err
	}
	if !created {
		return LoadDemoInstallation(baseRoot)
	}
	return state, nil
}

// MarkDemoUsed records, for good, that this installation's demo is spent.
func MarkDemoUsed(baseRoot, reason string) error {
	state, err := ensureDemoInstallation(baseRoot)
	if err != nil {
		return err
	}
	if state.Used {
		return nil
	}
	now := time.Now().UTC()
	state.Used, state.UsedReason, state.UsedAt = true, reason, &now
	return writeJSONAtomic(demoInstallationPath(baseRoot), state, 0o600)
}

// DemoServerProfile is the compiled-in profile below baseRoot.
func DemoServerProfile(baseRoot string) (ServerProfile, error) {
	serverDir, err := clientprofile.ServerDir(baseRoot, DemoServerID)
	if err != nil {
		return ServerProfile{}, err
	}
	return ServerProfile{ID: DemoServerID, Address: DemoServerAddress, KnownHostsPath: filepath.Join(serverDir, "known_hosts")}, nil
}

// SubmitDemoActivation asks the demo server for an OTP. A refusal comes back
// as *DemoRefusedError; anything else that is not an accepted frame is an
// error, exactly as for an invitation.
func SubmitDemoActivation(ctx context.Context, profile ServerProfile, email, installationUID, requestID string) (onboarding.OnboardResponse, error) {
	if err := profile.validate(); err != nil {
		return onboarding.OnboardResponse{}, err
	}
	canonical, err := onboarding.CanonicalEmail(email)
	if err != nil {
		return onboarding.OnboardResponse{}, err
	}
	request := onboarding.OnboardRequest{Schema: onboarding.DemoOnboardRequestSchema, Email: canonical, InstallationUID: installationUID, OnboardingRequestID: requestID}
	return judgeDemoResponse(request, func() (onboarding.OnboardResponse, error) { return exchangeOnboarding(ctx, profile, request) })
}

func judgeDemoResponse(request onboarding.OnboardRequest, exchange func() (onboarding.OnboardResponse, error)) (onboarding.OnboardResponse, error) {
	response, err := exchange()
	if err != nil {
		return onboarding.OnboardResponse{}, err
	}
	switch response.Status {
	case onboarding.DemoRefusedInstallation, onboarding.DemoRefusedAddress, onboarding.DemoRefusedPending, onboarding.DemoRefusedCapacity:
		if response.RetryAfterMinutes < 0 {
			return onboarding.OnboardResponse{}, errors.New("bootstrap response does not match request")
		}
		return onboarding.OnboardResponse{}, &DemoRefusedError{Code: response.Status, RetryAfterMinutes: response.RetryAfterMinutes}
	}
	if response.RetryAfterMinutes != 0 {
		return onboarding.OnboardResponse{}, errors.New("bootstrap response does not match request")
	}
	return acceptedOnboardResponse(request, response)
}

type demoSubmitter func(context.Context, ServerProfile, string, string, string) (onboarding.OnboardResponse, error)

// BeginDemo starts the demo activation of this installation for the given
// mailbox. It pins the compiled-in host key before any connection, refuses
// locally once the demo is spent, and records it as spent when the server
// says the installation has already had its realm.
func BeginDemo(ctx context.Context, baseRoot, email string) (OnboardPassport, ServerProfile, error) {
	return beginDemoWithSubmit(ctx, baseRoot, email, SubmitDemoActivation)
}

func beginDemoWithSubmit(ctx context.Context, baseRoot, email string, submit demoSubmitter) (OnboardPassport, ServerProfile, error) {
	baseRoot = filepath.Clean(baseRoot)
	state, err := ensureDemoInstallation(baseRoot)
	if err != nil {
		return OnboardPassport{}, ServerProfile{}, err
	}
	if offer, err := DemoActivationOffer(baseRoot); err != nil {
		return OnboardPassport{}, ServerProfile{}, err
	} else if state.Used || offer == DemoOfferUsed {
		return OnboardPassport{}, ServerProfile{}, ErrDemoUsed
	}
	canonical, err := onboarding.CanonicalEmail(email)
	if err != nil {
		return OnboardPassport{}, ServerProfile{}, err
	}
	profile, err := DemoServerProfile(baseRoot)
	if err != nil {
		return OnboardPassport{}, ServerProfile{}, err
	}
	root, err := profileStateRoot(baseRoot, profile)
	if err != nil {
		return OnboardPassport{}, ServerProfile{}, err
	}
	if _, err := os.Stat(filepath.Join(root, "client-profile.json")); err == nil {
		return OnboardPassport{}, ServerProfile{}, errors.New("the demo server is already activated on this installation")
	} else if !errors.Is(err, os.ErrNotExist) {
		return OnboardPassport{}, ServerProfile{}, err
	}
	if err := pinKnownHost(filepath.Join(root, "known_hosts"), DemoKnownHost); err != nil {
		return OnboardPassport{}, ServerProfile{}, err
	}
	// A request the server never accepted binds nothing; a different mailbox
	// simply starts over. An accepted one keeps its OTP and its address.
	passportPath := filepath.Join(root, "onboard.json")
	if existing, err := loadOnboardPassport(passportPath); err == nil && existing.State == passportPending && existing.Email != canonical {
		if err := os.Remove(passportPath); err != nil {
			return OnboardPassport{}, ServerProfile{}, err
		}
	}
	passport, err := beginOnboarding(ctx, baseRoot, profile, canonical, func(ctx context.Context, profile ServerProfile, email, requestID string) (onboarding.OnboardResponse, error) {
		return submit(ctx, profile, email, state.InstallationUID, requestID)
	})
	var refused *DemoRefusedError
	if errors.As(err, &refused) && refused.Code == onboarding.DemoRefusedInstallation {
		if markErr := MarkDemoUsed(baseRoot, refused.Code); markErr != nil {
			return OnboardPassport{}, ServerProfile{}, errors.Join(err, markErr)
		}
	}
	return passport, profile, err
}

// pinKnownHost writes the one accepted host key, or confirms the existing pin
// is the same; it never learns a key from the network.
func pinKnownHost(path, line string) error {
	want := line + "\n"
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) != want {
			return errors.New("the pinned host key conflicts with this server")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeBytesAtomic(path, []byte(want), 0o600)
}

// Demo offer states reported to the interface.
const (
	DemoOfferAvailable   = "available"
	DemoOfferUnavailable = "unavailable"
	DemoOfferUsed        = "used"
)

// DemoActivationOffer decides whether the interface offers the demo server.
// It is offered only to an installation with no activation at all. The first
// activation of any other server ends the offer for good, and so does the
// demo's own activation; while the demo itself is active the offer is merely
// unavailable, since its end is already recorded.
func DemoActivationOffer(baseRoot string) (string, error) {
	state, err := LoadDemoInstallation(baseRoot)
	if err != nil {
		return "", err
	}
	if state.Used {
		return DemoOfferUsed, nil
	}
	profiles, err := clientprofile.List(baseRoot)
	if err != nil {
		return "", err
	}
	servers := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		servers = append(servers, profile.ServerID)
	}
	return demoOfferFor(baseRoot, servers)
}

func demoOfferFor(baseRoot string, activatedServers []string) (string, error) {
	for _, server := range activatedServers {
		if server != DemoServerID {
			if err := MarkDemoUsed(baseRoot, "other_activation"); err != nil {
				return "", err
			}
			return DemoOfferUsed, nil
		}
	}
	if len(activatedServers) > 0 {
		return DemoOfferUnavailable, nil
	}
	return DemoOfferAvailable, nil
}
