package main

import (
	"context"
	"errors"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/config"
	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/errcat"
	"filees/pkg/passport"
	"github.com/google/uuid"
)

type passportProfileLookup func(string) (clientprofile.Profile, bool)

// Re-read the same activation's profile on every exchange. A reactivation must
// restart the repo; it must never silently rebind an old pending operation.
type passportProfileTransport struct {
	repo     config.Repo
	clientID string
	address  string
	port     int
	profiles passportProfileLookup
	build    func(controlclient.Config) (controlclient.Exchanger, error)
}

func (t passportProfileTransport) profile() (clientprofile.Profile, error) {
	if t.profiles == nil {
		return clientprofile.Profile{}, errcat.New(errcat.KeyPassportUnavailable, nil, nil)
	}
	p, ok := t.profiles(t.repo.ServerID)
	if !ok {
		return p, errcat.New(errcat.KeyPassportUnavailable, nil, nil)
	}
	if err := p.Validate(); err != nil {
		return p, errcat.New(errcat.KeyPassportUnavailable, nil, err)
	}
	if p.ServerID != t.repo.ServerID || (t.clientID != "" && p.ClientID != t.clientID) ||
		(t.address != "" && p.Address != t.address) || (t.port != 0 && p.SSHPort != t.port) ||
		(t.repo.SSHIdentityFile != "" && p.IdentityFile != t.repo.SSHIdentityFile) ||
		(t.repo.SSHKnownHosts != "" && p.KnownHosts != t.repo.SSHKnownHosts) {
		return p, errcat.New(errcat.KeyPassportRequestConflict, nil, nil)
	}
	return p, nil
}

func (t passportProfileTransport) Exchange(ctx context.Context, ticket control.Ticket) (control.Result, error) {
	p, err := t.profile()
	if err != nil {
		return control.Result{}, err
	}
	if ticket.ClientID != p.ClientID {
		return control.Result{}, errcat.New(errcat.KeyPassportRequestConflict, nil, nil)
	}
	build := t.build
	if build == nil {
		build = func(cfg controlclient.Config) (controlclient.Exchanger, error) { return controlclient.New(cfg) }
	}
	transport, err := build(controlclient.Config{Address: p.Address, Port: p.SSHPort, IdentityFile: p.IdentityFile, KnownHosts: p.KnownHosts, Timeout: 30 * time.Second})
	if err != nil {
		return control.Result{}, err
	} // retain unknown alpha diagnostics
	return transport.Exchange(ctx, ticket)
}

func newControlPassportBackend(repo config.Repo, svn client.Client, profiles passportProfileLookup) (passport.Backend, error) {
	if _, err := uuid.Parse(repo.ID); err != nil {
		return nil, errcat.New(errcat.KeyPassportUnavailable, nil, err)
	}
	if _, ok := svn.(client.LockReceiptReader); !ok {
		return nil, errors.New("SVN client cannot confirm local passport token")
	}
	transport := passportProfileTransport{repo: repo, profiles: profiles}
	p, err := transport.profile()
	if err != nil {
		return nil, err
	}
	transport.clientID = p.ClientID
	transport.address, transport.port = p.Address, p.SSHPort
	return passport.ControlSVNBackend{FenceAcquisitions: true, SVNBackend: passport.SVNBackend{Client: svn, WC: repo.LocalPath}, RepoID: repo.ID, ClientID: p.ClientID, Transport: transport}, nil
}
