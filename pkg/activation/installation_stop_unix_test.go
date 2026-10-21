//go:build linux || openbsd

package activation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/storagewatch"
	"github.com/google/uuid"
)

func TestInstallationStopRefusesNewSessionsWithoutChangingGrants(t *testing.T) {
	m, _ := newActivationTestManager(t)
	g := testActivationGrant(t, time.Now().Add(time.Hour))
	if err := m.Stage(g); err != nil {
		t.Fatal(err)
	}
	lease, err := m.ClaimSession(g.OperationID, g.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	stop := storagewatch.Gate{Root: m.config.Root}
	b := storagewatch.InstallationBinding{OperationID: uuid.NewString(), PreviousManifest: strings.Repeat("a", 64), CandidateManifest: strings.Repeat("b", 64)}
	if err := stop.RequestInstallationStop(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if l, err := m.ClaimSession(g.OperationID, g.ClientID); l != nil || !errors.Is(err, storagewatch.ErrInstallationStopped) {
		t.Fatal(l, err)
	}
	if !m.SessionAllowed(g.OperationID, g.ClientID) {
		t.Fatal("grant revoked by installation stop")
	}
	if err := m.SignalAllSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if revoked, err := lease.Revoked(); err != nil || !revoked {
		t.Fatal(revoked, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.WithAllSessionsStopped(t.Context(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if l, err := m.ClaimSession(g.OperationID, g.ClientID); l != nil || !errors.Is(err, storagewatch.ErrInstallationStopped) {
		t.Fatal("barrier release admitted new claim", err)
	}
}

func TestInstallationStopRechecksSessionAfterActivationLock(t *testing.T) {
	m, _ := newActivationTestManager(t)
	g := testActivationGrant(t, time.Now().Add(time.Hour))
	if err := m.Stage(g); err != nil {
		t.Fatal(err)
	}
	locked, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- withFileLockContext(t.Context(), filepath.Join(m.config.Root, ".activation.lock"), func() error { close(locked); <-release; return nil })
	}()
	<-locked
	claim := make(chan error, 1)
	go func() {
		l, err := m.ClaimSession(g.OperationID, g.ClientID)
		if l != nil {
			_ = l.Close()
		}
		claim <- err
	}()
	b := storagewatch.InstallationBinding{OperationID: uuid.NewString(), PreviousManifest: strings.Repeat("a", 64), CandidateManifest: strings.Repeat("b", 64)}
	err := (storagewatch.Gate{Root: m.config.Root}).RequestInstallationStop(context.Background(), b)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-claim; !errors.Is(err, storagewatch.ErrInstallationStopped) {
		t.Fatal("queued claim admitted", err)
	}
}
