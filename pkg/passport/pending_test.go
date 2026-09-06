package passport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"filees/pkg/client"
	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/errcat"
	"github.com/google/uuid"
)

type pendingClient struct {
	client.Client
	owner              string
	remote, local      *client.LockInfo
	locks, unlocks     int
	loseLock, denyLock bool
	confirmHook        func()
}

func (c *pendingClient) LockInfo(context.Context, string, string) (*client.LockInfo, error) {
	return c.remote, nil
}
func (c *pendingClient) LockWithComment(_ context.Context, _ string, _ []string, comment string, force bool) (string, error) {
	if force {
		panic("durable backend attempted force")
	}
	c.locks++
	if c.denyLock || c.remote != nil {
		return "", errors.New("not acquired")
	}
	c.remote = &client.LockInfo{Token: uuid.NewString(), Owner: c.owner, Comment: comment}
	copy := *c.remote
	c.local = &copy
	if c.loseLock {
		return "", errors.New("lost lock reply")
	}
	return "locked", nil
}
func (c *pendingClient) ConfirmLock(_ context.Context, _ string, _ string, comment string) (*client.LockInfo, error) {
	if c.confirmHook != nil {
		c.confirmHook()
	}
	if c.local == nil || c.remote == nil || c.local.Token != c.remote.Token || c.local.Comment != comment || c.remote.Comment != comment {
		return nil, nil
	}
	return c.remote, nil
}
func (c *pendingClient) Unlock(context.Context, string, []string) (string, error) {
	c.unlocks++
	c.remote, c.local = nil, nil
	return "", nil
}

type pendingTransport struct {
	t            *testing.T
	cli          *pendingClient
	wire         []byte
	result       control.Result
	calls        int
	lose         bool
	afterPrepare func()
	onReplay     func()
}

func (x *pendingTransport) Exchange(_ context.Context, ticket control.Ticket) (control.Result, error) {
	x.calls++
	raw, _ := json.Marshal(ticket)
	if x.wire != nil {
		if string(raw) != string(x.wire) {
			x.t.Fatal("retry minted a different ticket")
		}
		if x.onReplay != nil {
			x.onReplay()
		}
		return x.result, nil
	}
	x.wire = raw
	var p control.PreparePassportReplacementPayload
	if err := control.DecodePayload(ticket.Payload, &p); err != nil {
		x.t.Fatal(err)
	}
	if x.cli.remote == nil || p.ObservedLockID != x.cli.remote.Token {
		x.t.Fatal("prepare targets another lock")
	}
	x.cli.remote = nil
	x.result, _ = control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.PreparePassportReplacementResult{RepoID: p.RepoID, Path: p.Path, ObservedLockID: p.ObservedLockID, State: "prepared"}, time.Now())
	if x.afterPrepare != nil {
		x.afterPrepare()
	}
	if x.lose {
		return control.Result{}, errors.New("lost prepare reply")
	}
	return x.result, nil
}

type pendingFixture struct {
	cli                          *pendingClient
	transport                    *pendingTransport
	backend                      ControlSVNBackend
	store, path, instance, realm string
	now                          time.Time
}

func newPendingFixture(t *testing.T) *pendingFixture {
	f := &pendingFixture{store: filepath.Join(t.TempDir(), "passports.json"), instance: uuid.NewString(), realm: uuid.NewString(), now: time.Now().UTC()}
	f.cli = &pendingClient{owner: uuid.NewString()}
	f.transport = &pendingTransport{t: t, cli: f.cli}
	f.backend = ControlSVNBackend{SVNBackend: SVNBackend{Client: f.cli, WC: t.TempDir()}, ClientID: f.cli.owner, RepoID: uuid.NewString(), Transport: f.transport}
	f.path = filepath.Join(f.backend.WC, "Łódź.txt")
	return f
}
func (f *pendingFixture) open(t *testing.T) *Manager {
	t.Helper()
	m, err := Open(f.store, f.instance, f.backend, Config{Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func (f *pendingFixture) acquire(t *testing.T, m *Manager) Passport {
	t.Helper()
	ps, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm)
	if err != nil {
		t.Fatal(err)
	}
	return ps[0]
}

func TestAbortedPrepareNeedsDurableLocalRetirement(t *testing.T) {
	for _, failedSave := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "save-failure"}[failedSave], func(t *testing.T) {
			f := newPendingFixture(t)
			m := f.open(t)
			f.acquire(t, m)
			f.now = f.now.Add(11 * time.Minute)
			f.transport.lose = true
			if err := m.Heartbeat(t.Context()); err == nil {
				t.Fatal("lost prepare acknowledged")
			}
			p := m.Snapshot()[0]
			ticket := *p.Pending.Ticket
			f.transport.result, _ = control.NewErrorResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ErrorBody{Code: string(errcat.CodePassportAborted), Message: string(errcat.KeyPassportAborted)}, f.now)
			m = f.open(t)
			var projected []PendingStatus
			m.cfg.OnPending = func(rows []PendingStatus) { projected = rows }
			m.publishPendingLocked()
			if failedSave {
				f.transport.onReplay = func() {
					f.transport.onReplay = nil
					if err := os.Remove(f.store); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(f.store, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := m.Heartbeat(t.Context())
			if !controlclient.IsAbortedPreparation(err, ticket) {
				t.Fatalf("not a terminal receipt: %v", err)
			}
			if failedSave {
				if len(m.Snapshot()) != 1 || len(projected) != 1 {
					t.Fatal("failed save removed recovery fence")
				}
				if err := os.Remove(f.store); err != nil {
					t.Fatal(err)
				}
				if err := m.Heartbeat(t.Context()); !controlclient.IsAbortedPreparation(err, ticket) {
					t.Fatal(err)
				}
			}
			if len(m.Snapshot()) != 0 || len(projected) != 0 || len(f.open(t).Snapshot()) != 0 {
				t.Fatal("retirement not durable/projected")
			}
			if f.cli.locks != 1 || f.cli.unlocks != 0 {
				t.Fatal("retirement mutated SVN")
			}
			if err := m.Authorize(t.Context(), []string{f.path}); err == nil {
				t.Fatal("retirement authorized publication")
			}
		})
	}
}

func TestPendingHeartbeatReplaysExactTicketAfterRestart(t *testing.T) {
	f := newPendingFixture(t)
	m := f.open(t)
	first := f.acquire(t, m)
	f.now = f.now.Add(11 * time.Minute)
	f.transport.lose = true
	if err := m.Heartbeat(t.Context()); err == nil {
		t.Fatal("lost prepare acknowledged")
	}
	p := m.Snapshot()[0]
	if p.State != StatePending || p.Pending.Mode != "renew" || p.FencingToken != "" {
		t.Fatalf("pending=%+v", p)
	}
	// Snapshot does not expose pointers to the live intent/ticket.
	p.Pending.Ticket.RequestID = "tampered"
	p.Pending.Ticket.Payload[0] = '!'
	if err := m.Authorize(t.Context(), []string{f.path}); err == nil {
		t.Fatal("pending authorized")
	}
	if _, err := m.Release(t.Context(), []string{f.path}); err == nil {
		t.Fatal("pending released")
	}
	if err := m.ReleaseAll(t.Context()); err == nil || f.cli.unlocks != 0 {
		t.Fatal("shutdown discarded uncertainty")
	}
	m.ForgetRemoved([]string{f.path})
	if len(m.Snapshot()) != 1 {
		t.Fatal("pending forgotten")
	}
	m = f.open(t)
	if err := m.Heartbeat(t.Context()); err != nil {
		t.Fatal(err)
	}
	p = m.Snapshot()[0]
	if p.Pending != nil || p.State != StateActive || p.PassportID != first.PassportID || p.FencingToken == first.FencingToken || f.transport.calls != 2 || f.cli.locks != 2 {
		t.Fatalf("recovery=%+v calls=%d locks=%d", p, f.transport.calls, f.cli.locks)
	}
	if err := m.Authorize(t.Context(), []string{f.path}); err != nil {
		t.Fatal(err)
	}
}

func TestPendingLockReplyRecoveryIsReadOnly(t *testing.T) {
	for _, scenario := range []string{"own-receipt", "no-lock", "competitor", "copied-comment", "expired", "wrong-owner"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPendingFixture(t)
			f.cli.loseLock = true
			if scenario == "no-lock" {
				f.cli.denyLock = true
			}
			m := f.open(t)
			if _, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm); err == nil {
				t.Fatal("lost reply acknowledged")
			}
			p := m.Snapshot()[0]
			if p.Pending.Stage != "locking" {
				t.Fatal("missing acquisition fence")
			}
			switch scenario {
			case "competitor":
				f.cli.remote = &client.LockInfo{Token: "other", Owner: "other", Comment: "foreign"}
			case "copied-comment":
				f.cli.remote = &client.LockInfo{Token: "other", Owner: f.cli.owner, Comment: FormatComment(p.Pending.Metadata)}
			case "wrong-owner":
				f.cli.remote.Owner = "other"
			case "expired":
				f.now = p.ExpiresAt
			}
			m = f.open(t)
			_, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm)
			if (err == nil) != (scenario == "own-receipt") {
				t.Fatalf("recovery error=%v", err)
			}
			if f.cli.locks != 1 || f.transport.calls != 0 || f.cli.unlocks != 0 {
				t.Fatal("recovery mutated locks")
			}
		})
	}
}

func TestPendingPrepareConcurrentRetries(t *testing.T) {
	f := newPendingFixture(t)
	m := f.open(t)
	first := f.acquire(t, m)
	f.now = f.now.Add(11 * time.Minute)
	f.transport.lose = true
	if err := m.Heartbeat(t.Context()); err == nil {
		t.Fatal("expected disconnect")
	}
	m = f.open(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if f.cli.locks != 2 || f.transport.calls != 2 || m.Snapshot()[0].FencingToken == first.FencingToken {
		t.Fatal("concurrent retries repeated mutation")
	}
}

func TestPendingStorageAndBindingFences(t *testing.T) {
	for _, scenario := range []string{"before-intent", "after-lock", "legacy-backend", "changed-client", "changed-instance", "changed-path", "expired-prepare"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPendingFixture(t)
			m := f.open(t)
			if scenario == "before-intent" {
				if err := os.Mkdir(f.store, 0700); err != nil {
					t.Fatal(err)
				}
				if _, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm); err == nil || f.cli.locks != 0 {
					t.Fatal("mutation without durable intent")
				}
				return
			}
			if scenario == "expired-prepare" {
				f.acquire(t, m)
				f.now = f.now.Add(11 * time.Minute)
				f.transport.lose = true
				if err := m.Heartbeat(t.Context()); err == nil {
					t.Fatal("expected disconnect")
				}
				f.now = m.Snapshot()[0].ExpiresAt
				if err := f.open(t).Heartbeat(t.Context()); err == nil || f.transport.calls != 1 || f.cli.locks != 1 {
					t.Fatal("expired intent was renewed/reissued")
				}
				return
			}
			if scenario == "after-lock" {
				f.cli.confirmHook = func() {
					f.cli.confirmHook = nil
					if err := os.Remove(f.store); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(f.store, 0700); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				f.cli.loseLock = true
			}
			if _, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm); err == nil {
				t.Fatal("expected pending")
			}
			if m.Snapshot()[0].State != StatePending {
				t.Fatal("unsaved success authorized")
			}
			if scenario == "after-lock" {
				if err := os.Remove(f.store); err != nil {
					t.Fatal(err)
				}
				f.acquire(t, m)
				if f.cli.locks != 1 {
					t.Fatal("reacquired after failed receipt save")
				}
				return
			}
			var b Backend = f.backend
			switch scenario {
			case "legacy-backend":
				b = f.backend.SVNBackend
			case "changed-client":
				f.backend.ClientID = uuid.NewString()
				b = f.backend
			case "changed-instance":
				f.instance = uuid.NewString()
			case "changed-path":
				f.backend.WC = t.TempDir()
				b = f.backend
			}
			if _, err := Open(f.store, f.instance, b, Config{}); err == nil {
				t.Fatal("pending rebound to different identity/backend/WC")
			}
		})
	}
}

func TestPendingDoesNotExtendLifetimeAcrossSlowPrepare(t *testing.T) {
	f := newPendingFixture(t)
	m := f.open(t)
	f.acquire(t, m)
	f.now = f.now.Add(11 * time.Minute)
	f.transport.afterPrepare = func() { f.now = f.now.Add(time.Hour) }
	if err := m.Heartbeat(t.Context()); err == nil {
		t.Fatal("expired preparation proceeded to lock")
	}
	if f.cli.locks != 1 || m.Snapshot()[0].State != StatePending {
		t.Fatal("slow reply acquired or activated an expired intent")
	}
}

func TestPendingCrashBeforeLockDoesNotGuessAcquisition(t *testing.T) {
	f := newPendingFixture(t)
	m := f.open(t)
	meta := Metadata{PassportID: uuid.NewString(), InstanceUID: f.instance, RealmID: f.realm, IssuedAt: f.now, ExpiresAt: f.now.Add(time.Minute), HardExpiresAt: f.now.Add(time.Hour)}
	i, err := f.backend.NewLockIntent(f.path, meta, "acquire", f.now)
	if err != nil {
		t.Fatal(err)
	}
	i.Stage = "locking"
	m.passports[f.path] = Passport{Path: f.path, PassportID: meta.PassportID, InstanceUID: meta.InstanceUID, RealmID: meta.RealmID, IssuedAt: meta.IssuedAt, ExpiresAt: meta.ExpiresAt, HardExpiresAt: meta.HardExpiresAt, State: StatePending, Pending: &i}
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	m = f.open(t)
	if _, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm); err == nil || f.cli.locks != 0 {
		t.Fatal("restart guessed a never-sent lock was safe to repeat")
	}
}

func TestPendingLoadRejectsCorruptIntent(t *testing.T) {
	for _, scenario := range []string{"stage", "mode", "metadata", "ticket", "token", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPendingFixture(t)
			m := f.open(t)
			f.acquire(t, m)
			f.now = f.now.Add(11 * time.Minute)
			f.transport.lose = true
			if err := m.Heartbeat(t.Context()); err == nil {
				t.Fatal("expected pending")
			}
			list := m.Snapshot()
			switch scenario {
			case "stage":
				list[0].Pending.Stage = "finished"
			case "mode":
				list[0].Pending.Mode = "force"
			case "metadata":
				list[0].PassportID = uuid.NewString()
			case "ticket":
				list[0].Pending.Ticket.ClientID = uuid.NewString()
			case "token":
				list[0].FencingToken = "unconfirmed"
			case "duplicate":
				list = append(list, list[0])
			}
			raw, _ := json.Marshal(list)
			if err := os.WriteFile(f.store, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(f.store, f.instance, f.backend, Config{}); err == nil {
				t.Fatal("corrupt intent accepted")
			}
			if f.transport.calls != 1 || f.cli.locks != 1 {
				t.Fatal("load mutated state")
			}
		})
	}
}
