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
	var passport deploy.OnboardPassport
	var err error
	if strings.TrimSpace(payload.Invitation) != "" {
		passport, profile, err = deploy.BeginInvitation(ctx, payload.StateRoot, payload.Invitation)
	} else {
		passport, err = deploy.BeginOnboarding(ctx, payload.StateRoot, profile, payload.Email)
	}
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
	remotePort := payload.RemotePort
	if remotePort == 0 {
		remotePort = passport.RemotePort
	}
	if err := deploy.RunActivation(ctx, passport, deploy.ActivationOptions{Root: payload.StateRoot, ServerProfile: profile, RemotePort: remotePort}, otp); err != nil {
		return contract.ActivationCommandResult{}, err
	}
	return service.finalize(ctx, payload, passport)
}

func (service daemonActivationService) Pending(_ context.Context, payload contract.ActivationPendingPayload) (contract.ActivationPendingResult, error) {
	profiles, err := deploy.PendingInvitationActivations(payload.StateRoot)
	if err != nil {
		return contract.ActivationPendingResult{}, err
	}
	result := contract.ActivationPendingResult{Targets: make([]contract.ActivationTarget, 0, len(profiles))}
	for _, profile := range profiles {
		result.Targets = append(result.Targets, contract.ActivationTarget{ServerID: profile.ID, Address: profile.Address})
	}
	return result, nil
}

func (service daemonActivationService) Resume(ctx context.Context, payload contract.ActivationResumePayload) (contract.ActivationCommandResult, error) {
	profile := deploy.ServerProfile{ID: payload.ServerID, Address: payload.ServerAddress, KnownHostsPath: payload.KnownHostsPath}
	passport, err := deploy.LoadOnboardPassport(payload.StateRoot, profile)
	if err != nil {
		return contract.ActivationCommandResult{}, err
	}
	remotePort := payload.RemotePort
	if remotePort == 0 {
		remotePort = passport.RemotePort
	}
	if err := deploy.ResumeActivation(ctx, passport, deploy.ActivationOptions{Root: payload.StateRoot, ServerProfile: profile, RemotePort: remotePort}); err != nil {
		return contract.ActivationCommandResult{}, err
	}
	finish := contract.ActivationFinishPayload{ServerID: payload.ServerID, ServerAddress: payload.ServerAddress, KnownHostsPath: payload.KnownHostsPath, StateRoot: payload.StateRoot, RemotePort: remotePort}
	return service.finalize(ctx, finish, passport)
}

func (service daemonActivationService) finalize(ctx context.Context, payload contract.ActivationFinishPayload, passport deploy.OnboardPassport) (contract.ActivationCommandResult, error) {
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
		service.onActive(contract.ActivationStatus{ServerID: passport.ServerID, DisplayName: passport.ServerID, ClientRole: "normal", Address: payload.ServerAddress, ClientID: clientProfile.ClientID, SSHPort: clientProfile.SSHPort, SessionTimeoutMin: int(clientProfile.SVNTimeout() / time.Minute)})
	}
	return contract.ActivationCommandResult{ServerID: passport.ServerID, State: state}, nil
}

func prepareActivatedClientProfile(ctx context.Context, payload contract.ActivationFinishPayload) (clientprofile.Profile, error) {
	// Through ServerDir, like every other path built from a server ID since
	// r742. This was the one site left joining the ID raw, and it is the last
	// step of activation, so the failure it produced was the most expensive
	// shape available: the tunnel writes the identity into the encoded
	// directory, this looked for it in the unencoded one, found nothing, and
	// reported active_profile_pending - a state that returns no error, logs
	// nothing and shows nothing. Measured against cloud, whose server ID is
	// "atmprojekt:filees": on Windows that colon cannot even name a directory.
	root, err := clientprofile.ServerDir(payload.StateRoot, payload.ServerID)
	if err != nil {
		return clientprofile.Profile{}, err
	}
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
	// The service repo's authz has only ever granted a client read on its own
	// /clients/<id> subtree (pkg/activation.renderAccessLocked, unchanged
	// since r98) - root itself is always "* =" (deny). svnserve does not do a
	// masked/partial checkout of a denied root even when a child path is
	// granted, so pointing the working copy at repo root here made the very
	// first projection sync unconditionally fail for every fresh activation.
	// Scope the checkout to the one subtree the client is actually granted
	// (and the only thing ever read from it, view.json) instead.
	serviceURL := (&url.URL{Scheme: "svn+ssh", User: url.User(deploy.ServiceClientUser), Host: urlHost, Path: "/clients/" + identity.ClientID + "/"}).String()
	serviceWC := filepath.Join(root, "service-wc")
	profile := clientprofile.Profile{Schema: clientprofile.Schema, ServerID: payload.ServerID, DisplayName: payload.ServerID, Address: payload.ServerAddress, ClientID: identity.ClientID, IdentityFile: filepath.Join(identityRoot, "id_ed25519"), KnownHosts: filepath.Clean(payload.KnownHostsPath), SSHPort: port, ServiceURL: serviceURL, ServiceWC: serviceWC, RelativeViewPath: "view.json", CachePath: filepath.Join(root, "cache", "view.json"), PollInterval: time.Minute}
	if err := clientprofile.Store(filepath.Join(root, "client-profile.json"), profile); err != nil {
		return clientprofile.Profile{}, err
	}
	svn := client.New(client.Options{SvnPath: "svn", Timeout: profile.SVNTimeout(), LogScope: "svn:service:" + payload.ServerID, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, SSHPort: port})
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
