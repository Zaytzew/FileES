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

func (a clientActivator) Begin(parent context.Context, email string) error {
	if err := a.validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	_, err := a.client.ActivationBegin(ctx, contract.ActivationBeginPayload{ServerID: a.profile.ID, ServerAddress: a.profile.Address, KnownHostsPath: a.profile.KnownHostsPath, StateRoot: a.root, Email: email})
	return err
}

func (a clientActivator) Finish(parent context.Context, otp []byte) error {
	if err := a.validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	secret := string(otp)
	defer func() { secret = "" }()
	_, err := a.client.ActivationFinish(ctx, contract.ActivationFinishPayload{ServerID: a.profile.ID, ServerAddress: a.profile.Address, KnownHostsPath: a.profile.KnownHostsPath, StateRoot: a.root, RemotePort: a.remotePort, OTP: secret})
	return err
}

func (a clientActivator) validate() error {
	if a.client == nil || strings.TrimSpace(a.root) == "" || strings.TrimSpace(a.profile.ID) == "" || strings.TrimSpace(a.profile.Address) == "" || strings.TrimSpace(a.profile.KnownHostsPath) == "" {
		return errors.New("profil aktywacji nie jest skonfigurowany")
	}
	return nil
}
