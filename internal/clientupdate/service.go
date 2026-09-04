package clientupdate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"

	"filees/internal/releaseenvelope"
	contract "filees/pkg/contract/v1"
)

type Resolver interface {
	Resolve(context.Context, string, string, string) (*releaseenvelope.Resolved, error)
}

type Installer interface {
	Plan(context.Context, *releaseenvelope.Resolved) ([]contract.UpdateChange, bool, error)
	Apply(context.Context, *releaseenvelope.Resolved) error
}

type Service struct {
	Resolver       Resolver
	Installer      Installer
	State          StateStore
	Channel        string
	ChannelPath    string
	Component      string
	Platform       string
	CurrentVersion string

	mu sync.Mutex
	// appliedVersion bridges the short interval after an update replaces the
	// files but before the supervisor restarts this still-running process.
	appliedVersion string
}

func (service *Service) Status(ctx context.Context) (contract.UpdateStatus, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	resolved, state, err := service.resolve(ctx)
	if err != nil {
		return contract.UpdateStatus{}, err
	}
	current := service.currentVersion(state)
	status := contract.UpdateStatus{
		State: "current", Channel: service.Channel, CurrentVersion: current, ReleaseID: resolved.Envelope.ReleaseID,
		Summary: fmt.Sprintf("Podpisane wydanie %s, sequence %d", resolved.SigningKeyID, resolved.Envelope.Sequence),
	}
	if !sameClientVersion(resolved.Manifest.Version, current) {
		status.State = "available"
		status.AvailableVersion = resolved.Manifest.Version
		status.RestartRequired = true
	}
	return status, nil
}

func (service *Service) Plan(ctx context.Context) (contract.UpdatePlanResult, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	resolved, state, err := service.resolve(ctx)
	if err != nil {
		return contract.UpdatePlanResult{}, err
	}
	changes, restart, err := service.Installer.Plan(ctx, resolved)
	if err != nil {
		return contract.UpdatePlanResult{}, err
	}
	return contract.UpdatePlanResult{
		CurrentVersion: service.currentVersion(state), AvailableVersion: resolved.Manifest.Version,
		ReleaseID: resolved.Envelope.ReleaseID, Changes: changes, RestartRequired: restart,
	}, nil
}

func (service *Service) Apply(ctx context.Context) (contract.UpdateApplyResult, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	resolved, state, err := service.resolve(ctx)
	if err != nil {
		return contract.UpdateApplyResult{}, err
	}
	if sameClientVersion(resolved.Manifest.Version, service.currentVersion(state)) {
		return contract.UpdateApplyResult{InstalledVersion: resolved.Manifest.Version}, nil
	}
	_, restart, err := service.Installer.Plan(ctx, resolved)
	if err != nil {
		return contract.UpdateApplyResult{}, fmt.Errorf("refresh update plan: %w", err)
	}
	if err := service.Installer.Apply(ctx, resolved); err != nil {
		return contract.UpdateApplyResult{}, err
	}
	next, err := state.Advance(resolved.Envelope, resolved.Manifest.Version)
	if err != nil {
		return contract.UpdateApplyResult{}, err
	}
	if err := service.State.Save(next); err != nil {
		return contract.UpdateApplyResult{}, fmt.Errorf("persist update high-water mark: %w", err)
	}
	service.appliedVersion = resolved.Manifest.Version
	return contract.UpdateApplyResult{InstalledVersion: resolved.Manifest.Version, RestartRequired: restart}, nil
}

func (service *Service) resolve(ctx context.Context) (*releaseenvelope.Resolved, State, error) {
	if service.Resolver == nil || service.Installer == nil {
		return nil, State{}, errors.New("client update service is incomplete")
	}
	state, err := service.State.Load()
	if err != nil {
		return nil, State{}, err
	}
	resolved, err := service.Resolver.Resolve(ctx, service.ChannelPath, service.Component, service.Platform)
	if err != nil {
		return nil, State{}, err
	}
	if err := state.Check(resolved.Envelope); err != nil {
		return nil, State{}, err
	}
	return resolved, state, nil
}

func (service *Service) currentVersion(state State) string {
	if service.appliedVersion != "" {
		return canonicalClientVersion(service.appliedVersion)
	}
	// The binary that is actually running wins over persisted history. This is
	// what lets the updater repair an old MSI installed over a newer channel
	// release without lowering the anti-rollback high-water mark.
	if service.CurrentVersion != "" {
		return canonicalClientVersion(service.CurrentVersion)
	}
	if state.InstalledVersion != "" {
		return canonicalClientVersion(state.InstalledVersion)
	}
	return ""
}

func sameClientVersion(left, right string) bool {
	return canonicalClientVersion(left) == canonicalClientVersion(right)
}

// canonicalClientVersion maps the human-facing build stamp 0.1.15+r850 onto
// the numeric distribution/MSI form 0.1.15.850. Other version schemes remain
// untouched rather than being guessed at.
func canonicalClientVersion(value string) string {
	value = strings.TrimSpace(value)
	marker := strings.LastIndex(value, "+r")
	if marker <= 0 || marker+2 == len(value) {
		return value
	}
	for _, character := range value[marker+2:] {
		if !unicode.IsDigit(character) || character > unicode.MaxASCII {
			return value
		}
	}
	return value[:marker] + "." + value[marker+2:]
}
