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
)

// realmAliasService is the daemon's only bridge between local GUI IPC and
// the server's authenticated worker. It has no alias lookup operation.
type realmAliasService struct {
	provisioner *daemonProvisioner
	mu          sync.Mutex
	cache       map[string]ownerLabelCache
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
	return payload.Alias, nil
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

func (s *realmAliasService) ListRecipients(ctx context.Context, serverID string) ([]contract.RealmGrantRecipient, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return nil, fmt.Errorf("no activated profile for server %q", serverID)
	}
	result, err := s.exchange(ctx, profile, control.TicketListGrantRecipients, control.ListGrantRecipientsPayload{})
	if err != nil {
		return nil, err
	}
	var payload control.ListGrantRecipientsResult
	if err := control.DecodeResultPayload(result.Result, &payload); err != nil {
		return nil, err
	}
	recipients := make([]contract.RealmGrantRecipient, 0, len(payload.Recipients))
	for _, recipient := range payload.Recipients {
		recipients = append(recipients, contract.RealmGrantRecipient{RealmID: recipient.RealmID, Alias: recipient.Alias})
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

func (s *realmAliasService) Grant(ctx context.Context, serverID, repoID, recipientRealmID, access string) (contract.RealmGrantResult, error) {
	return s.realmGrantExchange(ctx, serverID, control.TicketGrantAccess, control.GrantAccessPayload{RepoID: repoID, RecipientRealmID: recipientRealmID, Access: access})
}

func (s *realmAliasService) Revoke(ctx context.Context, serverID, repoID, recipientRealmID string) (contract.RealmGrantResult, error) {
	return s.realmGrantExchange(ctx, serverID, control.TicketRevokeAccess, control.RevokeAccessPayload{RepoID: repoID, RecipientRealmID: recipientRealmID})
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
		return control.Result{}, errors.New("server rejected identity request")
	}
	return result, nil
}
