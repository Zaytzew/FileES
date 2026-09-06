package passport

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"filees/pkg/client"
)

type checkingClient struct {
	*pendingClient
	failure           error
	beforeObservation func()
}

func (c *checkingClient) ReadLockObservation(context.Context, string, string) (client.LockObservation, error) {
	if c.beforeObservation != nil {
		c.beforeObservation()
	}
	if c.failure != nil {
		return client.LockObservation{}, c.failure
	}
	return client.LockObservation{Local: c.local, Remote: c.remote}, nil
}

func TestCompletedAcquireReconcilesWithoutRepeatingLock(t *testing.T) {
	for _, scenario := range []string{"own", "absent", "competitor", "copied-comment", "missing-local", "expired-own", "expired-absent"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPendingFixture(t)
			c := &checkingClient{pendingClient: f.cli, failure: errors.New("lost observation")}
			f.backend.Client = c
			m := f.open(t)
			if _, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm); err == nil {
				t.Fatal("lost observation accepted")
			}
			p := m.Snapshot()[0]
			if p.Pending.Stage != "checking" {
				t.Fatal("completion not saved")
			}
			switch scenario {
			case "absent", "expired-absent":
				c.remote = nil
			case "competitor":
				c.remote = &client.LockInfo{Token: "other", Owner: "other", Comment: "other"}
			case "copied-comment":
				c.remote = &client.LockInfo{Token: "other", Owner: f.cli.owner, Comment: FormatComment(p.Pending.Metadata)}
			case "missing-local":
				c.local = nil
			}
			if scenario == "expired-own" || scenario == "expired-absent" {
				f.now = p.ExpiresAt.Add(time.Hour)
			}
			c.failure = nil
			m = f.open(t)
			_, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm)
			if (err == nil) != (scenario == "own") {
				t.Fatalf("result: %v", err)
			}
			gone := scenario == "absent" || scenario == "competitor" || scenario == "expired-absent"
			if (len(m.Snapshot()) == 0) != gone || (len(f.open(t).Snapshot()) == 0) != gone {
				t.Fatal("incorrect retirement")
			}
			if f.cli.locks != 1 || f.cli.unlocks != 0 || f.transport.calls != 0 || f.transport.cancels != 0 {
				t.Fatal("observation mutated")
			}
			if scenario != "own" && m.Authorize(t.Context(), []string{f.path}) == nil {
				t.Fatal("unproven publication")
			}
		})
	}
}

func TestCompletedAcquireRetirementSaveFailurePreservesPending(t *testing.T) {
	f := newPendingFixture(t)
	c := &checkingClient{pendingClient: f.cli, failure: errors.New("lost observation")}
	f.backend.Client = c
	m := f.open(t)
	var projected []PendingStatus
	m.cfg.OnPending = func(p []PendingStatus) { projected = p }
	if _, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm); err == nil {
		t.Fatal("missing observation")
	}
	c.failure = nil
	c.remote = nil
	c.beforeObservation = func() {
		c.beforeObservation = nil
		if err := os.Remove(f.store); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(f.store, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Heartbeat(t.Context()); err == nil || len(m.Snapshot()) != 1 || len(projected) != 1 || projected[0].Phase != "checking" {
		t.Fatal("failed save dropped pending")
	}
	if err := os.Remove(f.store); err != nil {
		t.Fatal(err)
	}
	if err := m.Heartbeat(t.Context()); err == nil || len(m.Snapshot()) != 0 || len(projected) != 0 || f.cli.locks != 1 {
		t.Fatal("retry did not settle read-only")
	}
}

func TestLockingAbsenceIsNotACompletionReceipt(t *testing.T) {
	f := newPendingFixture(t)
	c := &checkingClient{pendingClient: f.cli, beforeObservation: func() { t.Fatal("unproven acquisition used negative observation") }}
	f.backend.Client = c
	f.cli.loseLock = true
	m := f.open(t)
	if _, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm); err == nil {
		t.Fatal("lost acquisition acknowledged")
	}
	c.remote = nil
	m = f.open(t)
	if err := m.Heartbeat(t.Context()); err == nil || len(m.Snapshot()) != 1 || m.Snapshot()[0].Pending.Stage != "locking" {
		t.Fatal("absent lock erased in-flight fence")
	}
	if f.cli.locks != 1 || f.cli.unlocks != 0 {
		t.Fatal("recovery replayed mutation")
	}
}
