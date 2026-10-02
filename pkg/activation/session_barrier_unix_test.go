//go:build !windows

package activation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRealmSessionBarrierWaitsForAcknowledgementAndBlocksClaims(t *testing.T) {
	m, _ := newActivationTestManager(t)
	g := testActivationGrant(t, time.Now().Add(time.Hour))
	if err := m.Stage(g); err != nil {
		t.Fatal(err)
	}
	lease, err := m.ClaimSession(g.OperationID, g.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	other := newTestSessionLeaseAt(t, m.sessionRoot(), time.Now())
	t.Cleanup(func() { _ = other.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	published := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- m.WithRealmSessionsStopped(ctx, g.RealmID, func() error {
			close(published)
			<-release
			return nil
		})
	}()
	// Receiving the FIFO notification is not acknowledgement of child exit.
	for {
		revoked, err := lease.Revoked()
		if err != nil {
			t.Fatal(err)
		}
		if revoked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-published:
		t.Fatal("published before session closed")
	default:
	}
	claim := make(chan error, 1)
	go func() {
		l, err := m.ClaimSession(g.OperationID, g.ClientID)
		if l != nil {
			_ = l.Close()
		}
		claim <- err
	}()
	select {
	case <-claim:
		t.Fatal("new session bypassed barrier")
	case <-time.After(40 * time.Millisecond):
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-published:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-claim:
		t.Fatal("new session entered during publication")
	default:
	}
	if revoked, err := other.Revoked(); err != nil || revoked {
		t.Fatalf("other realm affected: revoked=%v err=%v", revoked, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-claim:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestRealmSessionBarrierNoAcknowledgementNeverPublishes(t *testing.T) {
	m, _ := newActivationTestManager(t)
	lease := newTestSessionLeaseAt(t, m.sessionRoot(), time.Now())
	t.Cleanup(func() { _ = lease.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 70*time.Millisecond)
	defer cancel()
	published := false
	err := m.WithRealmSessionsStopped(ctx, lease.Metadata.RealmID, func() error { published = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || published {
		t.Fatalf("err=%v published=%v", err, published)
	}
}

func TestRealmSessionBarrierDoesNotTreatOrphanAsAcknowledgement(t *testing.T) {
	m, _ := newActivationTestManager(t)
	lease := newTestSessionLeaseAt(t, m.sessionRoot(), time.Now())
	if err := lease.fifo.Close(); err != nil {
		t.Fatal(err)
	}
	lease.fifo = nil // crashed supervisor, no proof of child exit
	t.Cleanup(func() { _ = lease.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 70*time.Millisecond)
	defer cancel()
	err := m.WithRealmSessionsStopped(ctx, lease.Metadata.RealmID, func() error { t.Error("published for orphan"); return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(lease.Dir, sessionRecordName)); err != nil {
		t.Fatal(err)
	}
}

func TestRealmSessionBarrierPropagatesPublicationFailure(t *testing.T) {
	m, _ := newActivationTestManager(t)
	want := errors.New("publication failed")
	if err := m.WithRealmSessionsStopped(t.Context(), uuid.NewString(), func() error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
}
