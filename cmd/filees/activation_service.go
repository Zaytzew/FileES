package main

import (
	"bytes"
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
	onActive  func(contract.ActivationStatus)
	onProfile func(clientprofile.Profile)
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
	otp := append([]byte(nil), bytes.TrimSpace(payload.OTP)...)
	defer clear(otp)
	if err := deploy.RunActivation(ctx, passport, deploy.ActivationOptions{Root: payload.StateRoot, ServerProfile: profile, RemotePort: payload.RemotePort}, otp); err != nil {
		return contract.ActivationCommandResult{}, err
	}
	state := "active"
	clientProfile, profileErr := prepareActivatedClientProfile(ctx, payload)
	if clientProfile.ServerID != "" && service.onProfile != nil {
		service.onProfile(clientProfile)
	}
	if profileErr != nil {
		talk.With("activation:"+passport.ServerID).Warnf("client profile pending: %v", profileErr)
		state = "active_profile_pending"
	}
	if service.onActive != nil {
		service.onActive(contract.ActivationStatus{ServerID: passport.ServerID, DisplayName: passport.ServerID, ClientRole: "normal", Address: payload.ServerAddress, ClientID: clientProfile.ClientID, SSHPort: clientProfile.SSHPort})
	}
	return contract.ActivationCommandResult{ServerID: passport.ServerID, State: state}, nil
}

func prepareActivatedClientProfile(ctx context.Context, payload contract.ActivationFinishPayload) (clientprofile.Profile, error) {
	root := filepath.Join(filepath.Clean(payload.StateRoot), payload.ServerID)
	identityRoot := filepath.Join(root, "identity")
	identity, err := deploy.LoadActiveIdentity(identityRoot)
	if err != nil {
		return clientprofile.Profile{}, err
	}
	host, port, err := deploy.NormalizeServerAddress(payload.ServerAddress)
	if err != nil {
		return clientprofile.Profile{}, err
	}
	urlHost := host
	if strings.Contains(host, ":") {
		urlHost = "[" + host + "]"
	}
	serviceURL := (&url.URL{Scheme: "svn+ssh", User: url.User(deploy.ServiceClientUser), Host: urlHost, Path: "/"}).String()
	serviceWC := filepath.Join(root, "service-wc")
	profile := clientprofile.Profile{Schema: clientprofile.Schema, ServerID: payload.ServerID, DisplayName: payload.ServerID, Address: payload.ServerAddress, ClientID: identity.ClientID, IdentityFile: filepath.Join(identityRoot, "id_ed25519"), KnownHosts: filepath.Clean(payload.KnownHostsPath), SSHPort: port, ServiceURL: serviceURL, ServiceWC: serviceWC, RelativeViewPath: filepath.Join("clients", identity.ClientID, "view.json"), CachePath: filepath.Join(root, "cache", "view.json"), PollInterval: time.Minute}
	if err := clientprofile.Store(filepath.Join(root, "client-profile.json"), profile); err != nil {
		return clientprofile.Profile{}, err
	}
	svn := client.New(client.Options{SvnPath: "svn", Timeout: 30 * time.Minute, LogScope: "svn:service:" + payload.ServerID, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, SSHPort: port})
	if _, err := svn.Update(ctx, serviceWC); err == nil {
		return profile, nil
	}
	if err := os.MkdirAll(serviceWC, 0o700); err != nil {
		return clientprofile.Profile{}, err
	}
	_, err = svn.Checkout(ctx, serviceURL, serviceWC)
	if err != nil {
		return profile, err
	}
	return profile, nil
}
