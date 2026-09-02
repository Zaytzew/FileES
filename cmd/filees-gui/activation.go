package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"filees/internal/gui/actions"
	"filees/pkg/clientprofile"
	contract "filees/pkg/contract/v1"
	"filees/pkg/deploy"
	"filees/pkg/onboarding"
)

type activationClient interface {
	ActivationBegin(context.Context, contract.ActivationBeginPayload) (*contract.ActivationCommandResult, error)
	ActivationFinish(context.Context, contract.ActivationFinishPayload) (*contract.ActivationCommandResult, error)
	ActivationPending(context.Context, contract.ActivationPendingPayload) (*contract.ActivationPendingResult, error)
	ActivationResume(context.Context, contract.ActivationResumePayload) (*contract.ActivationCommandResult, error)
}

func (a clientActivator) Pending(parent context.Context) ([]actions.ActivationTarget, error) {
	if a.client == nil || strings.TrimSpace(a.root) == "" {
		return nil, errors.New("profil aktywacji nie jest skonfigurowany")
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	result, err := a.client.ActivationPending(ctx, contract.ActivationPendingPayload{StateRoot: a.root})
	if err != nil {
		return nil, err
	}
	targets := make([]actions.ActivationTarget, 0, len(result.Targets))
	for _, target := range result.Targets {
		targets = append(targets, actions.ActivationTarget{ServerID: target.ServerID, Address: target.Address})
	}
	return targets, nil
}

type clientActivator struct {
	client     activationClient
	root       string
	profile    deploy.ServerProfile
	remotePort int
}

func (a clientActivator) Begin(parent context.Context, wire string) (actions.ActivationTarget, error) {
	invitation, err := onboarding.DecodeInvitation(wire)
	if err != nil {
		return actions.ActivationTarget{}, err
	}
	profile := deploy.ServerProfile{ID: invitation.ServerID, Address: invitation.ServerAddress, KnownHostsPath: a.knownHostsPath(invitation.ServerID)}
	if err := a.validate(profile); err != nil {
		return actions.ActivationTarget{}, err
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	_, err = a.client.ActivationBegin(ctx, contract.ActivationBeginPayload{ServerID: profile.ID, ServerAddress: profile.Address, KnownHostsPath: profile.KnownHostsPath, StateRoot: a.root, Invitation: wire})
	if err != nil {
		return actions.ActivationTarget{}, err
	}
	return actions.ActivationTarget{ServerID: profile.ID, Address: profile.Address}, nil
}

func (a clientActivator) Finish(parent context.Context, target actions.ActivationTarget, otp []byte) error {
	profile := deploy.ServerProfile{ID: target.ServerID, Address: target.Address, KnownHostsPath: a.knownHostsPath(target.ServerID)}
	if err := a.validate(profile); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	secret := append(contract.Secret(nil), otp...)
	defer clear(secret)
	_, err := a.client.ActivationFinish(ctx, contract.ActivationFinishPayload{ServerID: profile.ID, ServerAddress: profile.Address, KnownHostsPath: profile.KnownHostsPath, StateRoot: a.root, OTP: secret})
	return err
}

func (a clientActivator) Resume(parent context.Context, target actions.ActivationTarget) error {
	profile := deploy.ServerProfile{ID: target.ServerID, Address: target.Address, KnownHostsPath: a.knownHostsPath(target.ServerID)}
	if err := a.validate(profile); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	_, err := a.client.ActivationResume(ctx, contract.ActivationResumePayload{ServerID: profile.ID, ServerAddress: profile.Address, KnownHostsPath: profile.KnownHostsPath, StateRoot: a.root})
	return err
}

func (a clientActivator) validate(profile deploy.ServerProfile) error {
	if a.client == nil || strings.TrimSpace(a.root) == "" || strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Address) == "" || strings.TrimSpace(profile.KnownHostsPath) == "" {
		return errors.New("profil aktywacji nie jest skonfigurowany")
	}
	return nil
}

// knownHostsPath resolves through clientprofile.ServerDir so this deprecated
// renderer agrees with the directory deploy actually creates. It is abandoned
// in favour of Wails, but a path that cannot exist is not the failure anyone
// should meet here either.
func (a clientActivator) knownHostsPath(serverID string) string {
	dir, err := clientprofile.ServerDir(a.root, serverID)
	if err != nil {
		return filepath.Join(filepath.Clean(a.root), serverID, "known_hosts")
	}
	return filepath.Join(dir, "known_hosts")
}
