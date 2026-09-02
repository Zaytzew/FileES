// Package clientactivation adapts the daemon activation contract to the
// presentation-neutral GUI action controller.
package clientactivation

import (
	"context"
	"errors"
	"path/filepath"

	"filees/pkg/clientprofile"
	"strings"
	"time"

	"filees/internal/gui/actions"
	contract "filees/pkg/contract/v1"
	"filees/pkg/onboarding"
)

type Client interface {
	ActivationBegin(context.Context, contract.ActivationBeginPayload) (*contract.ActivationCommandResult, error)
	ActivationFinish(context.Context, contract.ActivationFinishPayload) (*contract.ActivationCommandResult, error)
	ActivationPending(context.Context, contract.ActivationPendingPayload) (*contract.ActivationPendingResult, error)
	ActivationResume(context.Context, contract.ActivationResumePayload) (*contract.ActivationCommandResult, error)
}

type Activator struct {
	client Client
	root   string
}

func New(client Client, root string) *Activator {
	return &Activator{client: client, root: filepath.Clean(root)}
}

func (activator *Activator) Pending(parent context.Context) ([]actions.ActivationTarget, error) {
	if err := activator.validate("", ""); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	result, err := activator.client.ActivationPending(ctx, contract.ActivationPendingPayload{StateRoot: activator.root})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty activation inventory")
	}
	targets := make([]actions.ActivationTarget, 0, len(result.Targets))
	for _, target := range result.Targets {
		targets = append(targets, actions.ActivationTarget{ServerID: target.ServerID, Address: target.Address})
	}
	return targets, nil
}

func (activator *Activator) Begin(parent context.Context, wire string) (actions.ActivationTarget, error) {
	invitation, err := onboarding.DecodeInvitation(wire)
	if err != nil {
		return actions.ActivationTarget{}, err
	}
	if err := activator.validate(invitation.ServerID, invitation.ServerAddress); err != nil {
		return actions.ActivationTarget{}, err
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	_, err = activator.client.ActivationBegin(ctx, contract.ActivationBeginPayload{
		ServerID: invitation.ServerID, ServerAddress: invitation.ServerAddress,
		KnownHostsPath: activator.knownHostsPath(invitation.ServerID), StateRoot: activator.root, Invitation: wire,
	})
	if err != nil {
		return actions.ActivationTarget{}, err
	}
	return actions.ActivationTarget{ServerID: invitation.ServerID, Address: invitation.ServerAddress}, nil
}

func (activator *Activator) Finish(parent context.Context, target actions.ActivationTarget, otp []byte) error {
	if err := activator.validate(target.ServerID, target.Address); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	secret := append(contract.Secret(nil), otp...)
	defer clear(secret)
	_, err := activator.client.ActivationFinish(ctx, contract.ActivationFinishPayload{
		ServerID: target.ServerID, ServerAddress: target.Address,
		KnownHostsPath: activator.knownHostsPath(target.ServerID), StateRoot: activator.root, OTP: secret,
	})
	return err
}

func (activator *Activator) Resume(parent context.Context, target actions.ActivationTarget) error {
	if err := activator.validate(target.ServerID, target.Address); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	_, err := activator.client.ActivationResume(ctx, contract.ActivationResumePayload{
		ServerID: target.ServerID, ServerAddress: target.Address,
		KnownHostsPath: activator.knownHostsPath(target.ServerID), StateRoot: activator.root,
	})
	return err
}

// knownHostsPath goes through clientprofile.ServerDir so the pinned host key
// is looked for under the same name the state directory was created with. Both
// were built from the raw ID here, which worked until an ID contained
// something the filesystem reserves.
func (activator *Activator) knownHostsPath(serverID string) string {
	dir, err := clientprofile.ServerDir(activator.root, serverID)
	if err != nil {
		// validate() rejects an unusable ID before anything reaches this, so a
		// failure here would be a programming error rather than a bad input.
		// Returning the raw join keeps the caller's error message about the
		// path it could not open, which is the useful one.
		return filepath.Join(activator.root, serverID, "known_hosts")
	}
	return filepath.Join(dir, "known_hosts")
}

func (activator *Activator) validate(serverID, address string) error {
	if activator == nil || activator.client == nil || strings.TrimSpace(activator.root) == "" || activator.root == "." {
		return errors.New("profil aktywacji nie jest skonfigurowany")
	}
	if serverID != "" && (strings.TrimSpace(serverID) == "" || strings.TrimSpace(address) == "") {
		return errors.New("profil aktywacji nie jest skonfigurowany")
	}
	return nil
}
