//go:build !windows

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"filees/public-shares/abuse"
	"filees/public-shares/authority"
	"filees/public-shares/gate"
	"filees/public-shares/manifest"
)

type countedBackend struct {
	Backend
	calls int
}

type brokenVerifierBackend struct{ Backend }

func (b brokenVerifierBackend) Enter(ctx context.Context, alias, slug string) (authority.Entry, error) {
	entry, err := b.Backend.Enter(ctx, alias, slug)
	entry.Projection.PasswordHash = "malformed"
	return entry, err
}

func TestInvalidVerifierDoesNotBanVisitors(t *testing.T) {
	f := newWebFixture(t, nil)
	f.handler.Abuse, _ = abuse.New(nil)
	f.handler.Backend = brokenVerifierBackend{f.handler.Backend}
	for i := 0; i < abuse.FailureLimit+1; i++ {
		w := perform(f.handler, "POST", "/atmprojekt/przetarg-2026", "password=anything", map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		if w.Code != 404 {
			t.Fatalf("configuration failure punished visitor: %d", w.Code)
		}
	}
	if len(f.handler.Abuse.Snapshot()) != 0 {
		t.Fatal("configuration failure created ban")
	}
}

func (b *countedBackend) Enter(ctx context.Context, alias, slug string) (authority.Entry, error) {
	b.calls++
	return b.Backend.Enter(ctx, alias, slug)
}

func TestPasswordAbuseBlocksSourceBeforeBackend(t *testing.T) {
	hash, err := gate.HashPassword("correct-password", nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newWebFixture(t, func(s *manifest.Share) { s.Password = hash })
	f.handler.Abuse, _ = abuse.New(nil)
	b := &countedBackend{Backend: f.handler.Backend}
	f.handler.Backend = b
	request := func(ip, method, password string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/atmprojekt/przetarg-2026", strings.NewReader("password="+password))
		r.RemoteAddr = ip + ":123"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		return w
	}
	for i := 0; i < abuse.FailureLimit; i++ {
		w := request("198.51.100.1", "POST", "wrong-password")
		if w.Code != http.StatusNotFound {
			t.Fatalf("failure %d: %d", i, w.Code)
		}
	}
	before := b.calls
	for _, method := range []string{"GET", "POST"} {
		w := request("198.51.100.1", method, "correct-password")
		if w.Code != 429 || w.Header().Get("Retry-After") == "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("ban response: %v", w)
		}
	}
	if b.calls != before {
		t.Fatal("blocked source reached backend")
	}
	if w := request("198.51.100.2", "POST", "correct-password"); w.Code != http.StatusSeeOther {
		t.Fatalf("other source denied: %d", w.Code)
	}
}

func TestSourceGuardPrecedesEvenMissingBackend(t *testing.T) {
	f := newWebFixture(t, nil)
	f.handler.Abuse, _ = abuse.New(nil)
	// Exercise the early rejection without any backend available at all.
	ip := netip.MustParseAddr("198.51.100.4")
	for i := 0; i < abuse.FailureLimit; i++ {
		done, _ := f.handler.Abuse.Begin(ip)
		done(true)
	}
	f.handler.Backend = nil
	r := httptest.NewRequest("GET", "/anything/else", nil)
	r.RemoteAddr = ip.String() + ":123"
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 429 {
		t.Fatal("early guard absent")
	}
	// Invalid verifier must not be classified as a credential mismatch.
	projection := authority.Entry{}.Projection
	projection.PasswordHash = "malformed"
	if _, err := gate.Authorize(projection, "", "password"); err == gate.ErrPasswordMismatch {
		t.Fatal("invalid verifier counted as mismatch")
	}
}

func TestBusyDoesNotCountAsPasswordFailure(t *testing.T) {
	hash, err := gate.HashPassword("correct-password", nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newWebFixture(t, func(s *manifest.Share) { s.Password = hash })
	f.handler.Abuse, _ = abuse.New(nil)
	holdPasswordCheckSlots(t, passwordCheckConcurrency)
	for i := 0; i < abuse.FailureLimit+1; i++ {
		w := perform(f.handler, "POST", "/atmprojekt/przetarg-2026", "password=wrong", map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		if w.Code != 503 {
			t.Fatalf("busy counted: %d", w.Code)
		}
	}
	if len(f.handler.Abuse.Snapshot()) != 0 {
		t.Fatal("busy source banned")
	}
}
