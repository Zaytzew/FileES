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
	// report records a failure of the call to the daemon. It is injected by
	// the composition root rather than written here: internal/gui must not
	// reach the engine - watcher, commit, client, ipcserver, errmap - and the
	// operational log is an engine concern. Writing it directly held that
	// boundary open from r768 until the architecture test was run again, which
	// took twenty revisions because I kept running a narrow selection of tests.
	// A nil reporter simply records nothing.
	report func(step string, err error)
}

func New(client Client, root string) *Activator {
	return &Activator{client: client, root: filepath.Clean(root)}
}

// WithFailureReporter wires the activator to record failed daemon calls. Only
// the composition root knows where such a record belongs.
func (activator *Activator) WithFailureReporter(report func(step string, err error)) *Activator {
	activator.report = report
	return activator
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
	// The client deadline has to cover the same work the daemon is doing, and
	// that work reaches a remote server, deploys a worker and checks out a
	// working copy. At forty-five seconds the read deadline expired first and the
	// user got "i/o timeout" on a socket instead of anything about activation -
	// while the daemon carried on. Same shape as the recovery download before
	// r695: a budget set from hope rather than from the operation.
	ctx, cancel := context.WithTimeout(parent, activationStepDeadline)
	defer cancel()
	secret := append(contract.Secret(nil), otp...)
	defer clear(secret)
	_, err := activator.client.ActivationFinish(ctx, contract.ActivationFinishPayload{
		ServerID: target.ServerID, ServerAddress: target.Address,
		KnownHostsPath: activator.knownHostsPath(target.ServerID), StateRoot: activator.root, OTP: secret,
	})
	// The interface records only what the daemon cannot: that this call itself
	// failed. Measured on 2026-09-03 - the daemon was mid-activation and had
	// the real cause, this side gave up on the socket read, and the sentence
	// the user was shown ("i/o timeout") described neither. Nothing about
	// activation reached any log from here, so there was no way afterwards to
	// tell which of the two had spoken.
	activator.reportFailure("finish", err)
	return err
}

// reportFailure records the one failure the daemon cannot: that the call to it
// did not come back.
//
// Measured 2026-09-03 - the daemon was mid-activation and held the real cause,
// this side gave up on the socket read, and the sentence the user saw ("i/o
// timeout") described neither. Nothing about activation reached any log from
// here, so afterwards there was no telling which of the two had spoken.
//
// Success stays the daemon's to report, with its timing and resulting state:
// two records of one event invite the reader to trust the wrong one.
func (activator *Activator) reportFailure(step string, err error) {
	if err == nil || activator.report == nil {
		return
	}
	activator.report(step, err)
}

func (activator *Activator) Resume(parent context.Context, target actions.ActivationTarget) error {
	if err := activator.validate(target.ServerID, target.Address); err != nil {
		return err
	}
	// The client deadline has to cover the same work the daemon is doing, and
	// that work reaches a remote server, deploys a worker and checks out a
	// working copy. At forty-five seconds the read deadline expired first and the
	// user got "i/o timeout" on a socket instead of anything about activation -
	// while the daemon carried on. Same shape as the recovery download before
	// r695: a budget set from hope rather than from the operation.
	ctx, cancel := context.WithTimeout(parent, activationStepDeadline)
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

// activationStepDeadline covers pkg/ipcserver's activationDeadline with room to
// spare. It is not imported from there on purpose - the client bound must be at
// least the daemon's, so a timeout is reported as whatever actually went wrong
// rather than as a socket read expiring first, and a constant that can drift is
// easier to notice than a shared one that silently changes both ends.
//
// The daemon's bound came down to ten minutes once activation began reporting
// its own progress. This stays where it is: being generous costs nothing here,
// because the daemon now answers first with a real cause, and the only job left
// for this number is to not expire before it does.
const activationStepDeadline = 30 * time.Minute
