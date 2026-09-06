package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
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

func passportFixtureProfile(t *testing.T, serverID string) clientprofile.Profile {
	t.Helper()
	root := t.TempDir()
	return clientprofile.Profile{Schema: clientprofile.Schema, ServerID: serverID, ClientID: uuid.NewString(), Address: "127.0.0.1", SSHPort: 2222, IdentityFile: filepath.Join(root, "identity"), KnownHosts: filepath.Join(root, "known_hosts"), ServiceWC: filepath.Join(root, "service-wc"), CachePath: filepath.Join(root, "view.json"), RelativeViewPath: "view.json", ServiceURL: "svn+ssh://fixture/service", PollInterval: time.Minute}
}

type passportExchangeFunc func(context.Context, control.Ticket) (control.Result, error)

func (f passportExchangeFunc) Exchange(ctx context.Context, ticket control.Ticket) (control.Result, error) {
	return f(ctx, ticket)
}

func TestStarterPassportBackendUsesPinnedCurrentProfileWithoutFallback(t *testing.T) {
	p := passportFixtureProfile(t, "lab")
	repo := config.Repo{ID: uuid.NewString(), ServerID: p.ServerID, LocalPath: t.TempDir(), SSHIdentityFile: p.IdentityFile, SSHKnownHosts: p.KnownHosts}
	found := true
	lookup := func(string) (clientprofile.Profile, bool) { return p, found }
	b, err := newControlPassportBackend(repo, client.New(client.Options{}), lookup)
	if err != nil {
		t.Fatal(err)
	}
	backend, ok := b.(passport.ControlSVNBackend)
	if !ok || backend.ClientID != p.ClientID || backend.RepoID != repo.ID {
		t.Fatalf("legacy/wrong backend: %+v", b)
	}
	transport := backend.Transport.(passportProfileTransport)
	calls := 0
	transport.build = func(cfg controlclient.Config) (controlclient.Exchanger, error) {
		calls++
		if cfg.Address != p.Address || cfg.Port != p.SSHPort || cfg.IdentityFile != p.IdentityFile || cfg.KnownHosts != p.KnownHosts || cfg.Timeout != 30*time.Second {
			t.Fatalf("transport drift: %+v", cfg)
		}
		return passportExchangeFunc(func(_ context.Context, got control.Ticket) (control.Result, error) {
			return control.Result{OperationID: got.OperationID}, nil
		}), nil
	}
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketPreparePassportReplacement, p.ClientID, control.PreparePassportReplacementPayload{RepoID: repo.ID, Path: "doc", ObservedLockID: "token", PassportID: uuid.NewString(), InstanceUID: uuid.NewString(), Mode: "renew"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Exchange(t.Context(), ticket); err != nil {
		t.Fatal(err)
	}
	p.SessionTimeout = time.Hour // harmless profile edit must not discard the intent
	if _, err := transport.Exchange(t.Context(), ticket); err != nil {
		t.Fatal(err)
	}
	p.SSHPort = 2223
	if _, err := transport.Exchange(t.Context(), ticket); err == nil || calls != 2 {
		t.Fatal("control endpoint diverged from the running data client")
	}
	p.SSHPort = 2222
	p.ClientID = uuid.NewString()
	if _, err := transport.Exchange(t.Context(), ticket); err == nil || calls != 2 {
		t.Fatal("reactivation rebound an old operation")
	}
	found = false
	if _, err := transport.Exchange(t.Context(), ticket); err == nil || calls != 2 {
		t.Fatal("removed profile reused")
	}
	if _, err := newControlPassportBackend(repo, client.New(client.Options{}), lookup); err == nil {
		t.Fatal("missing profile selected legacy backend")
	}
	if _, _, err := backend.Lock(t.Context(), filepath.Join(repo.LocalPath, "doc"), "comment", true); err == nil {
		t.Fatal("intent bypass permitted")
	}
}

func TestStarterPassportBackendRejectsProfileAndClientMismatch(t *testing.T) {
	for _, scenario := range []string{"server", "identity", "pins", "no-profile", "invalid-profile", "no-receipt"} {
		t.Run(scenario, func(t *testing.T) {
			p := passportFixtureProfile(t, "lab")
			repo := config.Repo{ID: uuid.NewString(), ServerID: "lab", LocalPath: t.TempDir(), SSHIdentityFile: p.IdentityFile, SSHKnownHosts: p.KnownHosts}
			var cli client.Client = client.New(client.Options{})
			lookup := passportProfileLookup(func(string) (clientprofile.Profile, bool) { return p, true })
			switch scenario {
			case "server":
				p.ServerID = "another"
			case "identity":
				repo.SSHIdentityFile = filepath.Join(t.TempDir(), "other")
			case "pins":
				repo.SSHKnownHosts = filepath.Join(t.TempDir(), "other")
			case "no-profile":
				lookup = nil
			case "invalid-profile":
				p.ClientID = "invalid"
			case "no-receipt":
				cli = &recoveryClient{}
			}
			if _, err := newControlPassportBackend(repo, cli, lookup); err == nil {
				t.Fatal("unsafe backend accepted")
			}
		})
	}
}

func TestPassportTransportPreservesAlphaFailure(t *testing.T) {
	p := passportFixtureProfile(t, "lab")
	failure := errors.New("unknown control transport diagnostic")
	x := passportProfileTransport{repo: config.Repo{ServerID: "lab"}, clientID: p.ClientID, profiles: func(string) (clientprofile.Profile, bool) { return p, true }, build: func(controlclient.Config) (controlclient.Exchanger, error) { return nil, failure }}
	_, err := x.Exchange(t.Context(), control.Ticket{ClientID: p.ClientID})
	if !errors.Is(err, failure) {
		t.Fatalf("alpha diagnostic hidden: %v", err)
	}
	x.clientID = uuid.NewString()
	_, err = x.Exchange(t.Context(), control.Ticket{ClientID: p.ClientID})
	var fault errcat.Fault
	if !errors.As(err, &fault) || fault.Key != errcat.KeyPassportRequestConflict {
		t.Fatalf("identity drift not catalogued: %v", err)
	}
}
