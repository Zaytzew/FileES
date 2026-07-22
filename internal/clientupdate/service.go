package clientupdate

import (
	"context"
	"errors"
	"fmt"
	"sync"

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
	ChannelPath    string
	Component      string
	Platform       string
	CurrentVersion string

	mu sync.Mutex
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
		State: "current", CurrentVersion: current, ReleaseID: resolved.Envelope.ReleaseID,
		Summary: fmt.Sprintf("Podpisane wydanie %s, sequence %d", resolved.SigningKeyID, resolved.Envelope.Sequence),
	}
	if resolved.Manifest.Version != current {
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
	if resolved.Manifest.Version == service.currentVersion(state) {
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
	if state.InstalledVersion != "" {
		return state.InstalledVersion
	}
	return service.CurrentVersion
}
