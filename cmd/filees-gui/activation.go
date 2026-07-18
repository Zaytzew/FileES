package main

import (
	"context"
	"errors"
	"strings"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/deploy"
)

type activationClient interface {
	ActivationBegin(context.Context, contract.ActivationBeginPayload) (*contract.ActivationCommandResult, error)
	ActivationFinish(context.Context, contract.ActivationFinishPayload) (*contract.ActivationCommandResult, error)
}

type clientActivator struct {
	client     activationClient
	root       string
	profile    deploy.ServerProfile
	remotePort int
}

func (a clientActivator) Begin(parent context.Context, serverID, serverAddress, email string) error {
	profile := deploy.ServerProfile{ID: serverID, Address: serverAddress, KnownHostsPath: a.profile.KnownHostsPath}
	if err := a.validate(profile); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	_, err := a.client.ActivationBegin(ctx, contract.ActivationBeginPayload{ServerID: profile.ID, ServerAddress: profile.Address, KnownHostsPath: profile.KnownHostsPath, StateRoot: a.root, Email: email})
	return err
}

func (a clientActivator) Finish(parent context.Context, serverID, serverAddress string, otp []byte) error {
	profile := deploy.ServerProfile{ID: serverID, Address: serverAddress, KnownHostsPath: a.profile.KnownHostsPath}
	if err := a.validate(profile); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	secret := string(otp)
	defer func() { secret = "" }()
	_, err := a.client.ActivationFinish(ctx, contract.ActivationFinishPayload{ServerID: profile.ID, ServerAddress: profile.Address, KnownHostsPath: profile.KnownHostsPath, StateRoot: a.root, RemotePort: a.remotePort, OTP: secret})
	return err
}

func (a clientActivator) validate(profile deploy.ServerProfile) error {
	if a.client == nil || strings.TrimSpace(a.root) == "" || strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Address) == "" || strings.TrimSpace(profile.KnownHostsPath) == "" {
		return errors.New("profil aktywacji nie jest skonfigurowany")
	}
	return nil
}
