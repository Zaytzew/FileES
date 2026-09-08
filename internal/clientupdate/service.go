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
	appliedVersion   string
	appliedReleaseID string
	appliedRestart   bool
}

func (service *Service) Status(ctx context.Context) (contract.UpdateStatus, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.appliedRestart {
		return contract.UpdateStatus{
			State: "restart_required", Channel: service.Channel,
			CurrentVersion:   canonicalClientVersion(service.CurrentVersion),
			AvailableVersion: service.appliedVersion, ReleaseID: service.appliedReleaseID,
			Summary:         "Aktualizacja jest zainstalowana. Uruchom FileES ponownie, aby używać nowej wersji.",
			RestartRequired: true,
		}, nil
	}
	resolved, state, err := service.resolve(ctx)
	if err != nil {
		return contract.UpdateStatus{}, err
	}
	current := service.currentVersion(state)
	status := contract.UpdateStatus{
		State: "current", Channel: service.Channel, CurrentVersion: current, ReleaseID: resolved.Envelope.ReleaseID,
		Summary: fmt.Sprintf("Podpisane wydanie %s, sequence %d", resolved.SigningKeyID, resolved.Envelope.Sequence),
	}
	if clientUpdateAvailable(resolved.Manifest.Version, current) {
		status.State = "available"
		status.AvailableVersion = resolved.Manifest.Version
		status.RestartRequired = true
	}
	return status, nil
}

func (service *Service) Plan(ctx context.Context) (contract.UpdatePlanResult, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.appliedRestart {
		return contract.UpdatePlanResult{
			CurrentVersion:   canonicalClientVersion(service.CurrentVersion),
			AvailableVersion: service.appliedVersion, ReleaseID: service.appliedReleaseID,
			RestartRequired: true,
		}, nil
	}
	resolved, state, err := service.resolve(ctx)
	if err != nil {
		return contract.UpdatePlanResult{}, err
	}
	current := service.currentVersion(state)
	if !clientUpdateAvailable(resolved.Manifest.Version, current) {
		return contract.UpdatePlanResult{
			CurrentVersion: current, AvailableVersion: resolved.Manifest.Version,
			ReleaseID: resolved.Envelope.ReleaseID,
		}, nil
	}
	changes, restart, err := service.Installer.Plan(ctx, resolved)
	if err != nil {
		return contract.UpdatePlanResult{}, err
	}
	return contract.UpdatePlanResult{
		CurrentVersion: current, AvailableVersion: resolved.Manifest.Version,
		ReleaseID: resolved.Envelope.ReleaseID, Changes: changes, RestartRequired: restart,
	}, nil
}

func (service *Service) Apply(ctx context.Context) (contract.UpdateApplyResult, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.appliedRestart {
		return contract.UpdateApplyResult{InstalledVersion: service.appliedVersion, RestartRequired: true}, nil
	}
	resolved, state, err := service.resolve(ctx)
	if err != nil {
		return contract.UpdateApplyResult{}, err
	}
	current := service.currentVersion(state)
	if !clientUpdateAvailable(resolved.Manifest.Version, current) {
		return contract.UpdateApplyResult{InstalledVersion: current}, nil
	}
	_, restart, err := service.Installer.Plan(ctx, resolved)
	if err != nil {
		return contract.UpdateApplyResult{}, fmt.Errorf("refresh update plan: %w", err)
	}
	if err := service.Installer.Apply(ctx, resolved); err != nil {
		return contract.UpdateApplyResult{}, err
	}
	// The files have changed even if persisting the anti-rollback checkpoint
	// subsequently fails. Keep the restart latch for this process lifetime;
	// GUI reconnects and an unavailable release server must not erase it.
	service.appliedVersion = resolved.Manifest.Version
	service.appliedReleaseID = resolved.Envelope.ReleaseID
	service.appliedRestart = restart
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

// clientUpdateAvailable keeps a locally newer client from being offered (or
// applying) an older signed channel release. This matters for acceptance
// builds made from a revision ahead of the currently promoted bundle: a valid
// signature authenticates a release, but does not make it newer than the
// binary which is already running.
//
// FileES distribution versions are dot-separated numeric components. Unknown
// schemes retain the previous mismatch-means-update behaviour instead of being
// ordered by a guess.
func clientUpdateAvailable(available, current string) bool {
	if sameClientVersion(available, current) {
		return false
	}
	comparison, comparable := compareNumericClientVersions(available, current)
	if !comparable {
		return true
	}
	return comparison > 0
}

func compareNumericClientVersions(left, right string) (int, bool) {
	leftParts, leftOK := numericClientVersionParts(left)
	rightParts, rightOK := numericClientVersionParts(right)
	if !leftOK || !rightOK {
		return 0, false
	}
	count := len(leftParts)
	if len(rightParts) > count {
		count = len(rightParts)
	}
	for index := 0; index < count; index++ {
		leftPart, rightPart := "0", "0"
		if index < len(leftParts) {
			leftPart = leftParts[index]
		}
		if index < len(rightParts) {
			rightPart = rightParts[index]
		}
		if len(leftPart) < len(rightPart) {
			return -1, true
		}
		if len(leftPart) > len(rightPart) {
			return 1, true
		}
		if leftPart < rightPart {
			return -1, true
		}
		if leftPart > rightPart {
			return 1, true
		}
	}
	return 0, true
}

func numericClientVersionParts(value string) ([]string, bool) {
	parts := strings.Split(canonicalClientVersion(value), ".")
	for index, part := range parts {
		if part == "" {
			return nil, false
		}
		for _, character := range part {
			if !unicode.IsDigit(character) || character > unicode.MaxASCII {
				return nil, false
			}
		}
		part = strings.TrimLeft(part, "0")
		if part == "" {
			part = "0"
		}
		parts[index] = part
	}
	return parts, true
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
