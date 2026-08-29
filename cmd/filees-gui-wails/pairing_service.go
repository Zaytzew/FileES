package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	pairingSnapshotEvent = "filees:pairing-snapshot"
	pairingDisplayGrace  = 30 * time.Second
)

// PairingService owns the short-lived native Wails presentation of a mobile
// pairing QR. The secret is never published to the main dashboard and is
// removed from this service's snapshot as soon as the window closes.
type PairingService struct {
	gate         sync.Mutex
	mu           sync.RWMutex
	snapshot     PairingSnapshot
	emitter      snapshotEmitter
	show         func()
	hide         func()
	active       *pairingSession
	revision     uint64
	now          func() time.Time
	displayGrace time.Duration
}

type pairingSession struct {
	closed   chan struct{}
	resolved bool
}

type PairingSnapshot struct {
	Revision     uint64 `json:"revision"`
	Active       bool   `json:"active"`
	Address      string `json:"address,omitempty"`
	QRDataURL    string `json:"qr_data_url,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	DisplayUntil string `json:"display_until,omitempty"`
}

type PairingClose struct {
	Revision uint64 `json:"revision"`
}

type PairingAcceptance struct {
	Accepted bool   `json:"accepted"`
	Code     string `json:"code,omitempty"`
}

type pairingPresentation struct {
	Address   string
	QRDataURL string
	ExpiresAt time.Time
}

type pairingPresenter interface {
	Present(context.Context, pairingPresentation) error
}

// PairingBridge is the intentionally narrow browser surface. Present remains
// Go-only so a WebView cannot inject its own QR payload into this window.
type PairingBridge struct{ service *PairingService }

func newPairingBridge(service *PairingService) *PairingBridge {
	return &PairingBridge{service: service}
}

func (bridge *PairingBridge) Snapshot() PairingSnapshot { return bridge.service.Snapshot() }
func (bridge *PairingBridge) Close(request PairingClose) PairingAcceptance {
	return bridge.service.Close(request)
}
func (bridge *PairingBridge) Cancel() { bridge.service.Cancel() }

func newPairingService() *PairingService {
	return &PairingService{now: time.Now, displayGrace: pairingDisplayGrace}
}

func (service *PairingService) attachEmitter(emitter snapshotEmitter) {
	service.mu.Lock()
	service.emitter = emitter
	service.mu.Unlock()
}

func (service *PairingService) attachPresentation(show, hide func()) {
	service.mu.Lock()
	service.show, service.hide = show, hide
	service.mu.Unlock()
}

func (service *PairingService) Snapshot() PairingSnapshot {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.snapshot
}

func (service *PairingService) Close(request PairingClose) PairingAcceptance {
	service.mu.Lock()
	session := service.active
	if session == nil || request.Revision != service.snapshot.Revision {
		service.mu.Unlock()
		return PairingAcceptance{Code: "pairing_context_changed"}
	}
	if session.resolved {
		service.mu.Unlock()
		return PairingAcceptance{Code: "pairing_busy"}
	}
	session.resolved = true
	service.mu.Unlock()
	close(session.closed)
	return PairingAcceptance{Accepted: true}
}

func (service *PairingService) Cancel() {
	snapshot := service.Snapshot()
	if snapshot.Active {
		service.Close(PairingClose{Revision: snapshot.Revision})
	}
}

func (service *PairingService) Present(ctx context.Context, presentation pairingPresentation) error {
	if strings.TrimSpace(presentation.QRDataURL) == "" {
		return errors.New("empty mobile pairing QR")
	}
	service.gate.Lock()
	defer service.gate.Unlock()

	now := service.clock()()
	displayUntil := now.Add(service.grace())
	if !presentation.ExpiresAt.IsZero() && presentation.ExpiresAt.Before(displayUntil) {
		displayUntil = presentation.ExpiresAt
	}
	if !displayUntil.After(now) {
		return errors.New("mobile pairing token already expired")
	}

	session := &pairingSession{closed: make(chan struct{})}
	service.mu.Lock()
	service.revision++
	snapshot := PairingSnapshot{
		Revision: service.revision, Active: true, Address: presentation.Address,
		QRDataURL: presentation.QRDataURL, ExpiresAt: presentation.ExpiresAt.Format(time.RFC3339Nano),
		DisplayUntil: displayUntil.Format(time.RFC3339Nano),
	}
	service.snapshot, service.active = snapshot, session
	emitter, show := service.emitter, service.show
	service.mu.Unlock()
	if emitter != nil {
		emitter.Emit(pairingSnapshotEvent, snapshot)
	}
	if show != nil {
		show()
	}

	timer := time.NewTimer(time.Until(displayUntil))
	defer timer.Stop()
	var err error
	select {
	case <-session.closed:
	case <-timer.C:
	case <-ctx.Done():
		err = ctx.Err()
	}
	service.finish(session)
	return err
}

func (service *PairingService) finish(session *pairingSession) {
	service.mu.Lock()
	if service.active != session {
		service.mu.Unlock()
		return
	}
	service.active = nil
	service.revision++
	empty := PairingSnapshot{Revision: service.revision}
	service.snapshot = empty
	emitter, hide := service.emitter, service.hide
	service.mu.Unlock()
	if emitter != nil {
		emitter.Emit(pairingSnapshotEvent, empty)
	}
	if hide != nil {
		hide()
	}
}

func (service *PairingService) clock() func() time.Time {
	if service.now != nil {
		return service.now
	}
	return time.Now
}

func (service *PairingService) grace() time.Duration {
	if service.displayGrace > 0 {
		return service.displayGrace
	}
	return pairingDisplayGrace
}

var _ pairingPresenter = (*PairingService)(nil)
