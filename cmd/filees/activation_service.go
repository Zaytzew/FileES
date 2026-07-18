package main

import (
	"context"
	"strings"

	contract "filees/pkg/contract/v1"
	"filees/pkg/deploy"
)

type daemonActivationService struct {
	onActive func(contract.ActivationStatus)
}

func (service daemonActivationService) Begin(ctx context.Context, payload contract.ActivationBeginPayload) (contract.ActivationCommandResult, error) {
	profile := deploy.ServerProfile{ID: payload.ServerID, Address: payload.ServerAddress, KnownHostsPath: payload.KnownHostsPath}
	passport, err := deploy.BeginOnboarding(ctx, payload.StateRoot, profile, payload.Email)
	if err != nil {
		return contract.ActivationCommandResult{}, err
	}
	return contract.ActivationCommandResult{ServerID: passport.ServerID, State: "otp_required"}, nil
}

func (service daemonActivationService) Finish(ctx context.Context, payload contract.ActivationFinishPayload) (contract.ActivationCommandResult, error) {
	profile := deploy.ServerProfile{ID: payload.ServerID, Address: payload.ServerAddress, KnownHostsPath: payload.KnownHostsPath}
	passport, err := deploy.LoadOnboardPassport(payload.StateRoot, profile)
	if err != nil {
		return contract.ActivationCommandResult{}, err
	}
	otp := []byte(strings.TrimSpace(payload.OTP))
	defer clear(otp)
	if err := deploy.RunActivation(ctx, passport, deploy.ActivationOptions{Root: payload.StateRoot, ServerProfile: profile, RemotePort: payload.RemotePort}, otp); err != nil {
		return contract.ActivationCommandResult{}, err
	}
	if service.onActive != nil {
		service.onActive(contract.ActivationStatus{ServerID: passport.ServerID, DisplayName: payload.ServerAddress, ClientRole: "normal"})
	}
	return contract.ActivationCommandResult{ServerID: passport.ServerID, State: "active"}, nil
}
