package main

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	contract "filees/pkg/contract/v1"
	"filees/pkg/deploy"
	"filees/pkg/talk"
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
	state := "active"
	if err := prepareActivatedClientProfile(ctx, payload); err != nil {
		talk.With("activation:"+passport.ServerID).Warnf("client profile pending: %v", err)
		state = "active_profile_pending"
	}
	if service.onActive != nil {
		service.onActive(contract.ActivationStatus{ServerID: passport.ServerID, DisplayName: payload.ServerAddress, ClientRole: "normal"})
	}
	return contract.ActivationCommandResult{ServerID: passport.ServerID, State: state}, nil
}

func prepareActivatedClientProfile(ctx context.Context, payload contract.ActivationFinishPayload) error {
	root := filepath.Join(filepath.Clean(payload.StateRoot), payload.ServerID)
	identityRoot := filepath.Join(root, "identity")
	identity, err := deploy.LoadActiveIdentity(identityRoot)
	if err != nil {
		return err
	}
	host, port, err := deploy.NormalizeServerAddress(payload.ServerAddress)
	if err != nil {
		return err
	}
	urlHost := host
	if strings.Contains(host, ":") {
		urlHost = "[" + host + "]"
	}
	serviceURL := (&url.URL{Scheme: "svn+ssh", User: url.User(deploy.ServiceClientUser), Host: urlHost, Path: "/"}).String()
	serviceWC := filepath.Join(root, "service-wc")
	profile := clientprofile.Profile{Schema: clientprofile.Schema, ServerID: payload.ServerID, DisplayName: host, Address: payload.ServerAddress, ClientID: identity.ClientID, IdentityFile: filepath.Join(identityRoot, "id_ed25519"), KnownHosts: filepath.Clean(payload.KnownHostsPath), SSHPort: port, ServiceURL: serviceURL, ServiceWC: serviceWC, RelativeViewPath: filepath.Join("clients", identity.ClientID, "view.json"), CachePath: filepath.Join(root, "cache", "view.json"), PollInterval: time.Minute}
	if err := clientprofile.Store(filepath.Join(root, "client-profile.json"), profile); err != nil {
		return err
	}
	svn := client.New(client.Options{SvnPath: "svn", Timeout: 30 * time.Minute, LogScope: "svn:service:" + payload.ServerID, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, SSHPort: port})
	if _, err := svn.Update(ctx, serviceWC); err == nil {
		return nil
	}
	if err := os.MkdirAll(serviceWC, 0o700); err != nil {
		return err
	}
	_, err = svn.Checkout(ctx, serviceURL, serviceWC)
	return err
}
