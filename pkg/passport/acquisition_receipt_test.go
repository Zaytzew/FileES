package passport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
)

func TestAcquisitionReceiptAge(t *testing.T) {
	now := time.Now()
	for _, age := range []time.Duration{-time.Second, 0, 59 * time.Second, time.Minute, 2 * time.Minute} {
		if acquisitionReceiptFresh(now.Add(-age), now) != (age >= 0 && age < time.Minute) {
			t.Fatal(age)
		}
	}
	if acquisitionReceiptFresh(time.Time{}, now) {
		t.Fatal("missing start accepted")
	}
}

func TestAcquisitionReceiptDoesNotSurviveFailedCompletionSave(t *testing.T) {
	f := newPendingFixture(t)
	c := &receiptClient{pendingClient: f.cli}
	f.backend.Client = c
	m := f.open(t)
	original := m.storePath
	block := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(block, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	c.proofHook = func(*client.LockInfo) { m.storePath = filepath.Join(block, "store") }
	if _, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm); err == nil {
		t.Fatal("failed save accepted")
	}
	if m.passports[f.path].State != StatePending {
		t.Fatal("receipt became durable without save")
	}
	m.storePath = original
	c.proofHook = nil
	checks := 0
	f.cli.confirmHook = func() { checks++ }
	f.acquire(t, m)
	if checks == 0 || c.locks != 1 {
		t.Fatal("receipt reused after failed save", checks, c.locks)
	}
}

type receiptClient struct {
	*pendingClient
	proofHook func(*client.LockInfo)
	lost      bool
}

func (c *receiptClient) AcquireLock(ctx context.Context, wc, path, comment string) (*client.LockInfo, string, error) {
	out, err := c.LockWithComment(ctx, wc, []string{path}, comment, false)
	if err != nil {
		return nil, out, err
	}
	proof := *c.local
	if c.proofHook != nil {
		c.proofHook(&proof)
	}
	if c.lost {
		return &proof, out, errors.New("lost reply")
	}
	return &proof, out, nil
}

func TestImmediateAcquisitionReceiptSkipsOnlyItsConfirmation(t *testing.T) {
	f := newPendingFixture(t)
	c := &receiptClient{pendingClient: f.cli}
	f.backend.Client = c
	checks := 0
	f.cli.confirmHook = func() { checks++ }
	m := f.open(t)
	p := f.acquire(t, m)
	if p.State != StateActive || p.FencingToken != f.cli.local.Token || checks != 0 || c.locks != 1 {
		t.Fatal(p.State, checks, c.locks)
	}
}

func TestAcquisitionReceiptRejectsLostReplyBindingAndCancellation(t *testing.T) {
	for _, mode := range []string{"lost", "owner", "comment", "token", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newPendingFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := &receiptClient{pendingClient: f.cli, lost: mode == "lost"}
			c.proofHook = func(p *client.LockInfo) {
				switch mode {
				case "owner":
					p.Owner = "other"
				case "comment":
					p.Comment = "other"
				case "token":
					p.Token = ""
				case "cancel":
					cancel()
				}
			}
			f.backend.Client = c
			m := f.open(t)
			_, _, err := m.Acquire(ctx, []string{f.path}, f.realm)
			if err == nil {
				t.Fatal("invalid ACK accepted")
			}
			p := m.passports[f.path]
			if p.State != StatePending || p.Pending == nil || p.Pending.Stage != "locking" || c.locks != 1 {
				t.Fatal("lost recovery fence", p.State, c.locks)
			}
			// Restart must use independent confirmation, never replay acquisition.
			checks := 0
			f.cli.confirmHook = func() { checks++ }
			c.proofHook = nil
			c.lost = false
			m = f.open(t)
			p = f.acquire(t, m)
			if p.State != StateActive || checks == 0 || c.locks != 1 {
				t.Fatal("recovery bypassed", p.State, checks, c.locks)
			}
		})
	}
}
