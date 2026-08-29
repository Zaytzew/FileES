package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"filees/pkg/clientprofile"
	contract "filees/pkg/contract/v1"
	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/realmbranding"
)

// realmAliasService is the daemon's only bridge between local GUI IPC and
// the server's authenticated worker. It has no alias lookup operation.
type realmAliasService struct {
	provisioner *daemonProvisioner
	mu          sync.Mutex
	cache       map[string]ownerLabelCache
	// confirmedAliases is a short-lived bridge for an older server projection:
	// a successful claim is canonical confirmation, so an empty view from the
	// next polling tick must not erase it while that server publishes its
	// repaired view. Keys include the realm ID to avoid carrying an alias across
	// a later detach/join on the same server profile.
	projectionRealms map[string]string
	confirmedAliases map[realmAliasProjectionKey]string
}

type realmAliasProjectionKey struct {
	serverID string
	realmID  string
}

type ownerLabelCache struct {
	until  time.Time
	labels map[string]string
}

func (s *realmAliasService) Claim(ctx context.Context, serverID, alias string) (string, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return "", fmt.Errorf("no activated profile for server %q", serverID)
	}
	result, err := s.exchange(ctx, profile, control.TicketClaimRealmAlias, control.ClaimRealmAliasPayload{Alias: alias})
	if err != nil {
		return "", err
	}
	var payload control.ClaimRealmAliasResult
	if err := control.DecodeResultPayload(result.Result, &payload); err != nil {
		return "", err
	}
	s.rememberConfirmedAlias(serverID, payload.Alias)
	return payload.Alias, nil
}

func (s *realmAliasService) rememberConfirmedAlias(serverID, alias string) {
	if alias == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	realmID := s.projectionRealms[serverID]
	if realmID == "" {
		return
	}
	if s.confirmedAliases == nil {
		s.confirmedAliases = make(map[realmAliasProjectionKey]string)
	}
	s.confirmedAliases[realmAliasProjectionKey{serverID: serverID, realmID: realmID}] = alias
}

// ProjectAlias merges authoritative projection state with a successful claim
// made by this daemon process. A non-empty projected value always wins and is
// remembered; only an older empty projection receives the confirmed fallback.
func (s *realmAliasService) ProjectAlias(serverID, realmID, projected string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.projectionRealms == nil {
		s.projectionRealms = make(map[string]string)
	}
	s.projectionRealms[serverID] = realmID
	key := realmAliasProjectionKey{serverID: serverID, realmID: realmID}
	if projected != "" {
		if s.confirmedAliases == nil {
			s.confirmedAliases = make(map[realmAliasProjectionKey]string)
		}
		s.confirmedAliases[key] = projected
		return projected
	}
	return s.confirmedAliases[key]
}

func (s *realmAliasService) Resolve(ctx context.Context, serverID string, ownerIDs []string) (map[string]string, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return nil, fmt.Errorf("no activated profile for server %q", serverID)
	}
	validOwnerIDs := make([]string, 0, len(ownerIDs))
	for _, ownerID := range ownerIDs {
		if _, err := uuid.Parse(ownerID); err == nil {
			validOwnerIDs = append(validOwnerIDs, ownerID)
		}
	}
	if len(validOwnerIDs) == 0 {
		return map[string]string{}, nil
	}
	now := time.Now()
	missing := make([]string, 0, len(validOwnerIDs))
	labels := make(map[string]string, len(ownerIDs))
	s.mu.Lock()
	entry := s.cache[serverID]
	if now.Before(entry.until) {
		for _, ownerID := range validOwnerIDs {
			if label := entry.labels[ownerID]; label != "" {
				labels[ownerID] = label
			} else {
				missing = append(missing, ownerID)
			}
		}
	} else {
		missing = append(missing, validOwnerIDs...)
		entry = ownerLabelCache{labels: make(map[string]string)}
	}
	s.mu.Unlock()
	if len(missing) == 0 {
		return labels, nil
	}
	result, err := s.exchange(ctx, profile, control.TicketResolveOwnerLabels, control.ResolveOwnerLabelsPayload{OwnerIDs: missing})
	if err != nil {
		return nil, err
	}
	var payload control.ResolveOwnerLabelsResult
	if err := control.DecodeResultPayload(result.Result, &payload); err != nil {
		return nil, err
	}
	s.mu.Lock()
	entry = s.cache[serverID]
	if entry.labels == nil || now.After(entry.until) {
		entry.labels = make(map[string]string)
	}
	for ownerID, label := range payload.Labels {
		entry.labels[ownerID] = label
		labels[ownerID] = label
	}
	entry.until = now.Add(5 * time.Minute)
	s.cache[serverID] = entry
	s.mu.Unlock()
	return labels, nil
}

func (s *realmAliasService) ListRecipients(ctx context.Context, serverID, repoID string) ([]contract.RealmGrantRecipient, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return nil, fmt.Errorf("no activated profile for server %q", serverID)
	}
	result, err := s.exchange(ctx, profile, control.TicketListGrantRecipients, control.ListGrantRecipientsPayload{RepoID: repoID})
	if err != nil {
		return nil, err
	}
	var payload control.ListGrantRecipientsResult
	if err := control.DecodeResultPayload(result.Result, &payload); err != nil {
		return nil, err
	}
	recipients := make([]contract.RealmGrantRecipient, 0, len(payload.Recipients))
	for _, recipient := range payload.Recipients {
		recipients = append(recipients, contract.RealmGrantRecipient{RealmID: recipient.RealmID, Alias: recipient.Alias, Access: recipient.Access, State: recipient.State})
	}
	return recipients, nil
}

func (s *realmAliasService) SetVisibility(ctx context.Context, serverID, visibility string) (string, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return "", fmt.Errorf("no activated profile for server %q", serverID)
	}
	result, err := s.exchange(ctx, profile, control.TicketSetRealmVisibility, control.SetRealmDirectoryVisibilityPayload{Visibility: visibility})
	if err != nil {
		return "", err
	}
	var payload control.SetRealmDirectoryVisibilityResult
	if err := control.DecodeResultPayload(result.Result, &payload); err != nil {
		return "", err
	}
	return payload.Visibility, nil
}

func (s *realmAliasService) GetPublicBranding(ctx context.Context, serverID string) (realmbranding.Branding, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return realmbranding.Branding{}, fmt.Errorf("no activated profile for server %q", serverID)
	}
	result, err := s.exchange(ctx, profile, control.TicketGetRealmPublicBranding, struct{}{})
	if err != nil {
		return realmbranding.Branding{}, err
	}
	var payload control.RealmPublicBrandingResult
	if err := control.DecodeResultPayload(result.Result, &payload); err != nil {
		return realmbranding.Branding{}, err
	}
	return payload.Branding, nil
}

func (s *realmAliasService) SetPublicBranding(ctx context.Context, serverID string, branding realmbranding.Branding) (realmbranding.Branding, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return realmbranding.Branding{}, fmt.Errorf("no activated profile for server %q", serverID)
	}
	result, err := s.exchange(ctx, profile, control.TicketSetRealmPublicBranding, control.RealmPublicBrandingPayload{Branding: branding})
	if err != nil {
		return realmbranding.Branding{}, err
	}
	var payload control.RealmPublicBrandingResult
	if err := control.DecodeResultPayload(result.Result, &payload); err != nil {
		return realmbranding.Branding{}, err
	}
	return payload.Branding, nil
}

func (s *realmAliasService) Grant(ctx context.Context, serverID, repoID, recipientRealmID, access string) (contract.RealmGrantResult, error) {
	return s.realmGrantExchange(ctx, serverID, control.TicketGrantAccess, control.GrantAccessPayload{RepoID: repoID, RecipientRealmID: recipientRealmID, Access: access})
}

func (s *realmAliasService) Revoke(ctx context.Context, serverID, repoID, recipientRealmID string) (contract.RealmGrantResult, error) {
	return s.realmGrantExchange(ctx, serverID, control.TicketRevokeAccess, control.RevokeAccessPayload{RepoID: repoID, RecipientRealmID: recipientRealmID})
}

func (s *realmAliasService) ListPublicShares(ctx context.Context, serverID, repoID string) ([]contract.PublicShareSummary, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return nil, fmt.Errorf("no activated profile for server %q", serverID)
	}
	result, err := s.exchange(ctx, profile, control.TicketListPublicShares, control.ListPublicSharesPayload{RepoID: repoID})
	if err != nil {
		return nil, err
	}
	var payload control.ListPublicSharesResult
	if err := control.DecodeResultPayload(result.Result, &payload); err != nil {
		return nil, err
	}
	shares := make([]contract.PublicShareSummary, 0, len(payload.Shares))
	for _, share := range payload.Shares {
		shares = append(shares, publicShareSummaryFromControl(share))
	}
	return shares, nil
}

func (s *realmAliasService) CreatePublicShare(ctx context.Context, serverID string, declaration contract.PublicShareDeclaration) (contract.PublicShareResult, error) {
	return s.publicShareExchange(ctx, serverID, control.TicketCreatePublicShare, control.CreatePublicSharePayload{PublicShareDeclaration: publicShareDeclarationToControl(declaration)})
}

func (s *realmAliasService) UpdatePublicShare(ctx context.Context, serverID, channelID string, declaration contract.PublicShareDeclaration, keepPassword bool) (contract.PublicShareResult, error) {
	return s.publicShareExchange(ctx, serverID, control.TicketUpdatePublicShare, control.UpdatePublicSharePayload{ChannelID: channelID, KeepPassword: keepPassword, PublicShareDeclaration: publicShareDeclarationToControl(declaration)})
}

func (s *realmAliasService) RevokePublicShare(ctx context.Context, serverID, channelID string) (contract.PublicShareResult, error) {
	return s.publicShareExchange(ctx, serverID, control.TicketRevokePublicShare, control.RevokePublicSharePayload{ChannelID: channelID})
}

func (s *realmAliasService) DeletePublicShare(ctx context.Context, serverID, channelID string) (contract.PublicShareResult, error) {
	return s.publicShareExchange(ctx, serverID, control.TicketDeletePublicShare, control.DeletePublicSharePayload{ChannelID: channelID})
}

func (s *realmAliasService) publicShareExchange(ctx context.Context, serverID string, typ control.TicketType, payload any) (contract.PublicShareResult, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return contract.PublicShareResult{}, fmt.Errorf("no activated profile for server %q", serverID)
	}
	result, err := s.exchange(ctx, profile, typ, payload)
	if err != nil {
		return contract.PublicShareResult{}, err
	}
	var remote control.PublicShareResult
	if err := control.DecodeResultPayload(result.Result, &remote); err != nil {
		return contract.PublicShareResult{}, err
	}
	return contract.PublicShareResult{ChannelID: remote.ChannelID, Alias: remote.Alias, Slug: remote.Slug, State: remote.State, RecipientDeliveries: remote.RecipientDeliveries}, nil
}

func publicShareDeclarationToControl(declaration contract.PublicShareDeclaration) control.PublicShareDeclaration {
	objects := make([]control.PublicShareObject, 0, len(declaration.Objects))
	for _, object := range declaration.Objects {
		objects = append(objects, control.PublicShareObject{PublicID: object.PublicID, RepoPath: object.RepoPath, DisplayName: object.DisplayName, Size: object.Size})
	}
	return control.PublicShareDeclaration{RepoID: declaration.RepoID, SourceRoot: declaration.SourceRoot, Slug: declaration.Slug, Recipients: append([]string(nil), declaration.Recipients...), PasswordHash: declaration.PasswordHash, DoNotFollow: declaration.DoNotFollow, Objects: objects}
}

func (s *realmAliasService) ListUploadChannels(ctx context.Context, serverID, repoID string) ([]contract.UploadChannelSummary, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return nil, fmt.Errorf("no activated profile for server %q", serverID)
	}
	result, err := s.exchange(ctx, profile, control.TicketListUploadChannels, control.ListUploadChannelsPayload{AuthorityRepoID: repoID})
	if err != nil {
		return nil, err
	}
	var payload control.ListUploadChannelsResult
	if err := control.DecodeResultPayload(result.Result, &payload); err != nil {
		return nil, err
	}
	channels := make([]contract.UploadChannelSummary, 0, len(payload.Channels))
	for _, channel := range payload.Channels {
		channels = append(channels, uploadChannelSummaryFromControl(channel))
	}
	return channels, nil
}

func (s *realmAliasService) CreateUploadChannel(ctx context.Context, serverID string, declaration contract.UploadChannelDeclaration) (contract.UploadChannelResult, error) {
	return s.uploadChannelExchange(ctx, serverID, control.TicketCreateUploadChannel, control.CreateUploadChannelPayload{UploadChannelDeclaration: uploadChannelDeclarationToControl(declaration)})
}

func (s *realmAliasService) UpdateUploadChannel(ctx context.Context, serverID, channelID string, declaration contract.UploadChannelDeclaration) (contract.UploadChannelResult, error) {
	return s.uploadChannelExchange(ctx, serverID, control.TicketUpdateUploadChannel, control.UpdateUploadChannelPayload{ChannelID: channelID, UploadChannelDeclaration: uploadChannelDeclarationToControl(declaration)})
}

func (s *realmAliasService) RevokeUploadChannel(ctx context.Context, serverID, channelID string) (contract.UploadChannelResult, error) {
	return s.uploadChannelExchange(ctx, serverID, control.TicketRevokeUploadChannel, control.RevokeUploadChannelPayload{ChannelID: channelID})
}

func (s *realmAliasService) DeleteUploadChannel(ctx context.Context, serverID, channelID string) (contract.UploadChannelResult, error) {
	return s.uploadChannelExchange(ctx, serverID, control.TicketDeleteUploadChannel, control.DeleteUploadChannelPayload{ChannelID: channelID})
}

func (s *realmAliasService) uploadChannelExchange(ctx context.Context, serverID string, typ control.TicketType, payload any) (contract.UploadChannelResult, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return contract.UploadChannelResult{}, fmt.Errorf("no activated profile for server %q", serverID)
	}
	result, err := s.exchange(ctx, profile, typ, payload)
	if err != nil {
		return contract.UploadChannelResult{}, err
	}
	var remote control.UploadChannelResult
	if err := control.DecodeResultPayload(result.Result, &remote); err != nil {
		return contract.UploadChannelResult{}, err
	}
	return contract.UploadChannelResult{ChannelID: remote.ChannelID, Alias: remote.Alias, Slug: remote.Slug, State: remote.State, UploadRepoID: remote.UploadRepoID, RecipientDeliveries: remote.RecipientDeliveries}, nil
}

func uploadChannelDeclarationToControl(declaration contract.UploadChannelDeclaration) control.UploadChannelDeclaration {
	return control.UploadChannelDeclaration{AuthorityRepoID: declaration.AuthorityRepoID, Slug: declaration.Slug, Kind: declaration.Kind, Recipients: append([]string(nil), declaration.Recipients...), RequireOTP: declaration.RequireOTP}
}

func uploadChannelSummaryFromControl(channel control.UploadChannelSummary) contract.UploadChannelSummary {
	return contract.UploadChannelSummary{
		ChannelID: channel.ChannelID, AuthorityRepoID: channel.AuthorityRepoID, UploadRepoID: channel.UploadRepoID,
		Alias: channel.Alias, Slug: channel.Slug, Kind: channel.Kind, State: channel.State,
		Recipients: append([]string(nil), channel.Recipients...), RequireOTP: channel.RequireOTP, UpdatedAt: channel.UpdatedAt,
	}
}

func publicShareSummaryFromControl(share control.PublicShareSummary) contract.PublicShareSummary {
	objects := make([]contract.PublicShareObject, 0, len(share.Objects))
	for _, object := range share.Objects {
		objects = append(objects, contract.PublicShareObject{PublicID: object.PublicID, RepoPath: object.RepoPath, DisplayName: object.DisplayName, Size: object.Size})
	}
	return contract.PublicShareSummary{ChannelID: share.ChannelID, RepoID: share.RepoID, Alias: share.Alias, Slug: share.Slug, State: share.State, SourceRoot: share.SourceRoot, Recipients: append([]string(nil), share.Recipients...), PasswordProtected: share.PasswordProtected, DoNotFollow: share.DoNotFollow, Objects: objects, UpdatedAt: share.UpdatedAt}
}

// SetEditingPolicy forwards the owner's choice to the server, which is the
// only party that can decide it: ownership lives in the canonical repository
// record, not in anything the client holds.
func (s *realmAliasService) SetEditingPolicy(ctx context.Context, serverID, repoID, policy string) (string, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return "", fmt.Errorf("no activated profile for server %q", serverID)
	}
	result, err := s.exchange(ctx, profile, control.TicketSetRepositoryEditingPolicy, control.SetRepositoryEditingPolicyPayload{RepoID: repoID, Policy: policy})
	if err != nil {
		return "", err
	}
	var remote control.SetRepositoryEditingPolicyResult
	if err := control.DecodeResultPayload(result.Result, &remote); err != nil {
		return "", err
	}
	return remote.Policy, nil
}

func (s *realmAliasService) realmGrantExchange(ctx context.Context, serverID string, typ control.TicketType, payload any) (contract.RealmGrantResult, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return contract.RealmGrantResult{}, fmt.Errorf("no activated profile for server %q", serverID)
	}
	result, err := s.exchange(ctx, profile, typ, payload)
	if err != nil {
		return contract.RealmGrantResult{}, err
	}
	var remote control.RealmGrantResult
	if err := control.DecodeResultPayload(result.Result, &remote); err != nil {
		return contract.RealmGrantResult{}, err
	}
	return contract.RealmGrantResult{RepoID: remote.RepoID, RecipientRealmID: remote.RecipientRealmID, Access: remote.Access, State: remote.State}, nil
}

func (s *realmAliasService) exchange(ctx context.Context, profile clientprofile.Profile, typ control.TicketType, payload any) (control.Result, error) {
	transport, err := controlclient.New(controlclient.Config{
		Address: profile.Address, Port: profile.SSHPort, IdentityFile: profile.IdentityFile,
		KnownHosts: profile.KnownHosts, Timeout: 30 * time.Second,
	})
	if err != nil {
		return control.Result{}, err
	}
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), typ, profile.ClientID, payload, time.Now())
	if err != nil {
		return control.Result{}, err
	}
	result, err := transport.Exchange(ctx, ticket)
	if err != nil {
		return control.Result{}, err
	}
	if result.Status != control.ResultOK {
		if result.Error != nil {
			return control.Result{}, fmt.Errorf("server rejected identity request: %s: %s", result.Error.Code, result.Error.Message)
		}
		return control.Result{}, errors.New("server rejected identity request")
	}
	return result, nil
}
