package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	control "filees/pkg/control/v1"
	contract "filees/pkg/contract/v1"
	"filees/pkg/controlclient"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// mobilePairingService implements ipcserver.MobilePairingService: it mints a
// MOBILE_PAIRING token through the daemon's own already-activated
// control-plane identity (mirrors repository_provisioner.go's
// controlclient.Exchange pattern for CREATE_REPOSITORY), then completes the
// result with locally-known address/host-key fields the server-side ticket
// result does not carry.
type mobilePairingService struct {
	provisioner *daemonProvisioner
}

func (s mobilePairingService) Begin(ctx context.Context, serverID string) (contract.MobilePairingBeginResult, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return contract.MobilePairingBeginResult{}, fmt.Errorf("no activated profile for server %q", serverID)
	}
	transport, err := controlclient.New(controlclient.Config{Address: profile.Address, Port: profile.SSHPort, IdentityFile: profile.IdentityFile, KnownHosts: profile.KnownHosts, Timeout: 30 * time.Second})
	if err != nil {
		return contract.MobilePairingBeginResult{}, err
	}
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketMobilePairing, profile.ClientID, control.MobilePairingPayload{}, time.Now())
	if err != nil {
		return contract.MobilePairingBeginResult{}, err
	}
	result, err := transport.Exchange(ctx, ticket)
	if err != nil {
		return contract.MobilePairingBeginResult{}, err
	}
	if result.Status != control.ResultOK {
		msg := "mobile pairing request failed"
		if result.Error != nil {
			msg = result.Error.Message
		}
		return contract.MobilePairingBeginResult{}, errors.New(msg)
	}
	var payload control.MobilePairingResult
	if err := control.DecodeResultPayload(result.Result, &payload); err != nil {
		return contract.MobilePairingBeginResult{}, err
	}
	hostKey, err := readPinnedHostKey(profile.KnownHosts)
	if err != nil {
		return contract.MobilePairingBeginResult{}, err
	}
	return contract.MobilePairingBeginResult{Token: payload.Token, ExpiresAt: payload.ExpiresAt, Address: profile.Address, HostPublicKey: hostKey}, nil
}

// readPinnedHostKey extracts the single pinned host key from a known_hosts
// file as a bare authorized-keys line ("ssh-ed25519 AAAA..."), the exact
// shape pkg/mobileclient/sshtransport.New requires and the already-shipped
// Android client expects in its QR payload's host_public_key field.
func readPinnedHostKey(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	_, _, pubKey, _, _, err := ssh.ParseKnownHosts(raw)
	if err != nil {
		return "", fmt.Errorf("no usable host key entry in %s: %w", path, err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubKey))), nil
}
