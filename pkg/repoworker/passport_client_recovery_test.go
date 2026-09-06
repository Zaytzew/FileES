package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/errcat"
	"filees/pkg/passport"
	"github.com/google/uuid"
)

type recoveryExchange func(context.Context, control.Ticket) (control.Result, error)

func TestPassportInterruptedPrepareRetiresTicketRealSVN(t *testing.T) {
	for _, afterUnlock := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-unlock", true: "after-unlock"}[afterUnlock], func(t *testing.T) {
			f, wc, doc := realReplacementFixture(t)
			root := t.TempDir()
			svc := &PassportPreparations{Root: root, Authority: f.authority}
			actualRun := runLockAuthorityCommand
			mutations := 0
			svc.Authority.Locks.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if args[0] == "unlock" {
					if afterUnlock {
						if _, err := actualRun(ctx, name, args...); err != nil {
							return nil, err
						}
						mutations++
					}
					return nil, errors.New("fixture worker interrupted")
				}
				return actualRun(ctx, name, args...)
			}
			var old control.Ticket
			dropAbort := true
			transport := recoveryExchange(func(ctx context.Context, ticket control.Ticket) (control.Result, error) {
				if old.OperationID == "" {
					old = ticket
				}
				r, err := svc.Handle(ctx, f.session, ticket)
				if err == nil && r.Error != nil && r.Error.Code == string(errcat.CodePassportAborted) && dropAbort {
					dropAbort = false
					return control.Result{}, errors.New("fixture lost terminal receipt")
				}
				return r, err
			})
			cli := &recoverySVNClient{replacementSVNClient: &replacementSVNClient{Client: client.New(client.Options{Timeout: 10 * time.Second}), username: f.session.ClientID}}
			backend := passport.ControlSVNBackend{SVNBackend: passport.SVNBackend{Client: cli, WC: wc}, RepoID: f.req.RepoID, ClientID: f.session.ClientID, Transport: transport}
			store, instance := filepath.Join(t.TempDir(), "passports.json"), uuid.NewString()
			open := func() *passport.Manager {
				m, err := passport.Open(store, instance, backend, passport.Config{})
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			m := open()
			if _, _, err := m.Acquire(t.Context(), []string{doc}, f.owner); err == nil {
				t.Fatal("interruption acknowledged")
			}
			svc = &PassportPreparations{Root: root, Authority: f.authority}
			m = open()
			if err := m.Heartbeat(t.Context()); err == nil || len(m.Snapshot()) != 1 {
				t.Fatal("lost terminal receipt discarded pending")
			}
			m = open()
			if err := m.Heartbeat(t.Context()); !controlclient.IsAbortedPreparation(err, old) {
				t.Fatalf("terminal receipt: %v", err)
			}
			if len(m.Snapshot()) != 0 || len(open().Snapshot()) != 0 || cli.locks != 0 {
				t.Fatal("retirement acquired or retained intent")
			}
			if err := m.Authorize(t.Context(), []string{doc}); err == nil {
				t.Fatal("retirement granted publication")
			}
			if afterUnlock && mutations != 1 {
				t.Fatal("wrong mutation count")
			}
			// A fresh user/pipeline attempt starts with a new inspection and intent.
			if _, _, err := m.Acquire(t.Context(), []string{doc}, f.owner); err != nil {
				t.Fatal(err)
			}
			before := m.Snapshot()[0].FencingToken
			for range 3 {
				r, err := svc.Handle(t.Context(), f.session, old)
				requirePreparationCode(t, r, err, errcat.CodePassportAborted)
			}
			lock, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
			if err != nil || lock == nil || lock.Token != before || cli.locks != 1 || cli.forceCalls != 0 {
				t.Fatalf("old ticket damaged new lock: %+v %v", lock, err)
			}
			data, err := os.ReadFile(doc)
			if err != nil || string(data) != "local work" {
				t.Fatalf("local bytes changed: %q %v", data, err)
			}
		})
	}
}

func (f recoveryExchange) Exchange(ctx context.Context, ticket control.Ticket) (control.Result, error) {
	return f(ctx, ticket)
}

type recoverySVNClient struct {
	*replacementSVNClient
	loseReply bool
	locks     int
}

func (c *recoverySVNClient) LockWithComment(ctx context.Context, wc string, paths []string, comment string, force bool) (string, error) {
	c.locks++
	out, err := c.replacementSVNClient.LockWithComment(ctx, wc, paths, comment, force)
	if err == nil && c.loseReply {
		return out, errors.New("fixture lost SVN lock response")
	}
	return out, err
}
func (c *recoverySVNClient) ConfirmLock(ctx context.Context, wc, path, comment string) (*client.LockInfo, error) {
	return c.Client.(client.LockReceiptReader).ConfirmLock(ctx, wc, path, comment)
}

func TestPassportManagerControlRecoveryRealSVN(t *testing.T) {
	for _, scenario := range []string{"own-receipt", "competitor", "copied-comment"} {
		t.Run(scenario, func(t *testing.T) {
			f, wc, doc := realReplacementFixture(t)
			root := t.TempDir()
			svc := &PassportPreparations{Root: root, Authority: f.authority}
			seen := map[string]string{}
			calls, dropReply := 0, true
			transport := recoveryExchange(func(ctx context.Context, ticket control.Ticket) (control.Result, error) {
				calls++
				raw, _ := json.Marshal(ticket)
				if previous, ok := seen[ticket.OperationID]; ok && previous != string(raw) {
					t.Fatal("changed ticket on retry")
				}
				seen[ticket.OperationID] = string(raw)
				r, err := (&Worker{PassportPreparations: svc}).Handle(ctx, f.session, ticket)
				if err != nil {
					return r, err
				}
				if r.Status != control.ResultOK {
					t.Fatalf("server denied preparation: %+v", r)
				}
				if dropReply {
					dropReply = false
					return control.Result{}, errors.New("fixture lost control response")
				}
				return r, nil
			})
			cli := &recoverySVNClient{replacementSVNClient: &replacementSVNClient{Client: client.New(client.Options{Timeout: 10 * time.Second}), username: f.session.ClientID}, loseReply: true}
			backend := passport.ControlSVNBackend{SVNBackend: passport.SVNBackend{Client: cli, WC: wc}, RepoID: f.req.RepoID, ClientID: f.session.ClientID, Transport: transport}
			store, instance := filepath.Join(t.TempDir(), "passports.json"), uuid.NewString()
			now := time.Now().UTC()
			open := func() *passport.Manager {
				t.Helper()
				m, err := passport.Open(store, instance, backend, passport.Config{Now: func() time.Time { return now }})
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			m := open()
			if _, _, err := m.Acquire(t.Context(), []string{doc}, f.owner); err == nil {
				t.Fatal("control loss acknowledged")
			}
			p := m.Snapshot()[0]
			if p.Pending == nil || p.Pending.Mode != "migrate" || cli.locks != 0 {
				t.Fatal("migration intent lost")
			}
			// New worker object and new Manager recover their independently durable state.
			svc = &PassportPreparations{Root: root, Authority: f.authority}
			otherWC := filepath.Join(t.TempDir(), "other-wc")
			replacementCommand(t, "svn", "checkout", "file://"+filepath.Join(f.authority.Locks.RepositoriesRoot, f.req.RepoID), otherWC)
			otherDoc := filepath.Join(otherWC, filepath.FromSlash(f.req.Path))
			if scenario != "own-receipt" {
				comment := "competing lock"
				if scenario == "copied-comment" {
					comment = passport.FormatComment(p.Pending.Metadata)
				}
				replacementCommand(t, "svn", "lock", "--username", f.session.ClientID, "-m", comment, otherDoc)
			}
			m = open()
			if _, _, err := m.Acquire(t.Context(), []string{doc}, f.owner); err == nil {
				t.Fatal("lost/failed lock acknowledged")
			}
			before, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
			if err != nil || before == nil {
				t.Fatalf("lock missing: %+v %v", before, err)
			}
			m = open()
			_, _, err = m.Acquire(t.Context(), []string{doc}, f.owner)
			if (err == nil) != (scenario == "own-receipt") {
				t.Fatalf("recovery=%v", err)
			}
			after, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
			if err != nil || after == nil || after.Token != before.Token || cli.locks != 1 || cli.forceCalls != 0 || calls != 2 {
				t.Fatalf("recovery mutated: before=%+v after=%+v locks=%d control=%d %v", before, after, cli.locks, calls, err)
			}
			if scenario == "own-receipt" {
				if err := m.Authorize(t.Context(), []string{doc}); err != nil {
					t.Fatal(err)
				}
				proof, err := cli.ConfirmLock(t.Context(), otherWC, otherDoc, before.Comment)
				if err != nil || proof != nil {
					t.Fatalf("sibling WC claimed a remote-only token: %+v %v", proof, err)
				}
				// A regular heartbeat must now create a NEW renewal, not replay the migration.
				now = now.Add(11 * time.Minute)
				svc.Authority.Now = func() time.Time { return now }
				cli.loseReply = false
				if err := m.Heartbeat(t.Context()); err != nil {
					t.Fatal(err)
				}
				if len(seen) != 2 || cli.locks != 2 || m.Snapshot()[0].FencingToken == before.Token {
					t.Fatal("renewal did not rotate token")
				}
			} else if err := m.Authorize(t.Context(), []string{doc}); err == nil {
				t.Fatal("foreign reservation authorized")
			}
			data, err := os.ReadFile(doc)
			if err != nil || string(data) != "local work" {
				t.Fatalf("local bytes changed: %q %v", data, err)
			}
		})
	}
}
