package passport

import (
	"github.com/google/uuid"
	"os"
	"testing"
	"time"
)

func TestExpiredPrepareCancellationSurvivesRestartAndClockRollback(t *testing.T) {
	f := newPendingFixture(t)
	m := f.open(t)
	f.acquire(t, m)
	f.now = f.now.Add(11 * time.Minute)
	f.transport.lose = true
	if err := m.Heartbeat(t.Context()); err == nil {
		t.Fatal("lost prepare acknowledged")
	}
	f.now = m.Snapshot()[0].ExpiresAt
	f.transport.loseCancel = true
	if err := m.Heartbeat(t.Context()); err == nil {
		t.Fatal("lost cancel acknowledged")
	}
	if m.Snapshot()[0].Pending.Stage != "canceling" || f.transport.calls != 1 || f.transport.cancels != 1 {
		t.Fatal("cancellation direction not saved")
	}
	f.now = f.now.Add(-time.Hour)
	m = f.open(t)
	f.transport.loseCancel = false
	if err := m.Heartbeat(t.Context()); err == nil {
		t.Fatal("cancellation must not report acquisition")
	}
	if len(m.Snapshot()) != 0 || len(f.open(t).Snapshot()) != 0 || f.transport.calls != 1 || f.transport.cancels != 2 || f.cli.locks != 1 || f.cli.unlocks != 0 {
		t.Fatal("recovery replayed preparation/acquisition")
	}
}

func TestCanceledPrepareLocalSaveFailureRetainsFence(t *testing.T) {
	f := newPendingFixture(t)
	m := f.open(t)
	f.acquire(t, m)
	f.now = f.now.Add(11 * time.Minute)
	f.transport.lose = true
	if err := m.Heartbeat(t.Context()); err == nil {
		t.Fatal("lost prepare acknowledged")
	}
	f.now = m.Snapshot()[0].ExpiresAt
	f.transport.cancelHook = func() {
		f.transport.cancelHook = nil
		if err := os.Remove(f.store); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(f.store, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Heartbeat(t.Context()); err == nil {
		t.Fatal("failed retirement acknowledged")
	}
	if len(m.Snapshot()) != 1 || m.Snapshot()[0].Pending.Stage != "canceling" {
		t.Fatal("save failure lost cancellation")
	}
	if err := os.Remove(f.store); err != nil {
		t.Fatal(err)
	}
	if err := m.Heartbeat(t.Context()); err == nil {
		t.Fatal("cancellation reported acquisition")
	}
	if len(m.Snapshot()) != 0 || f.transport.calls != 1 || f.transport.cancels != 2 || f.cli.locks != 1 {
		t.Fatal("retry failed")
	}
}

func TestUnsentFreshAcquireCanExpireWithoutNetwork(t *testing.T) {
	f := newPendingFixture(t)
	m := f.open(t)
	meta := Metadata{PassportID: uuid.NewString(), InstanceUID: f.instance, RealmID: f.realm, IssuedAt: f.now, ExpiresAt: f.now.Add(time.Minute), HardExpiresAt: f.now.Add(time.Hour)}
	i, err := f.backend.NewLockIntent(f.path, meta, "acquire", f.now)
	if err != nil {
		t.Fatal(err)
	}
	p := Passport{Path: f.path, PassportID: meta.PassportID, InstanceUID: meta.InstanceUID, RealmID: meta.RealmID, IssuedAt: meta.IssuedAt, ExpiresAt: meta.ExpiresAt, HardExpiresAt: meta.HardExpiresAt, State: StatePending, Pending: &i}
	m.passports[f.path] = p
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	f.now = meta.ExpiresAt
	m = f.open(t)
	if err := m.Heartbeat(t.Context()); err == nil {
		t.Fatal("expired acquire reported success")
	}
	if len(m.Snapshot()) != 0 || f.cli.locks != 0 || f.transport.calls != 0 || f.transport.cancels != 0 {
		t.Fatal("unsent acquisition contacted server")
	}
	i.Stage = "locking"
	if err := f.backend.CancelLockIntent(t.Context(), f.path, i); err == nil {
		t.Fatal("locking was treated as unsent")
	}
}
