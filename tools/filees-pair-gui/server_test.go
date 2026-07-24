package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"filees/pkg/localpin"
)

func testPayload() handoff {
	return handoff{Address: "biuro.example.net:22", HostPublicKey: "ssh-ed25519 AAAA...", Token: "OTP-TOKEN", ExpiresAt: time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339Nano)}
}

func newTestServer(t *testing.T) (*server, *localpin.Store) {
	t.Helper()
	pin, err := localpin.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := newSessionPath()
	if err != nil {
		t.Fatal(err)
	}
	return newServer(pin, testPayload(), session, nil, nil), pin
}

func TestIndexOffersSetupWhenNoPINConfigured(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/"+srv.session+"/", nil)
	rec := httptest.NewRecorder()
	srv.mux().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), srv.session+"/setup") {
		t.Fatalf("index (unconfigured) code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestIndexOffersVerifyWhenPINConfigured(t *testing.T) {
	srv, pin := newTestServer(t)
	if err := pin.Setup([]byte("4242")); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/"+srv.session+"/", nil)
	rec := httptest.NewRecorder()
	srv.mux().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), srv.session+"/verify") {
		t.Fatalf("index (configured) code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestQRRouteForbiddenBeforeVerification(t *testing.T) {
	srv, pin := newTestServer(t)
	if err := pin.Setup([]byte("4242")); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/"+srv.session+"/qr.png", nil)
	rec := httptest.NewRecorder()
	srv.mux().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("qr.png before verify: code=%d", rec.Code)
	}
}

func TestVerifyWrongPINDoesNotUnlockQR(t *testing.T) {
	srv, pin := newTestServer(t)
	if err := pin.Setup([]byte("4242")); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"pin": {"0000"}}
	req := httptest.NewRequest("POST", "/"+srv.session+"/verify", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux().ServeHTTP(rec, req)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "qr.png") {
		t.Fatalf("wrong PIN verify: code=%d body=%s", rec.Code, rec.Body.String())
	}
	qrReq := httptest.NewRequest("GET", "/"+srv.session+"/qr.png", nil)
	qrRec := httptest.NewRecorder()
	srv.mux().ServeHTTP(qrRec, qrReq)
	if qrRec.Code != 403 {
		t.Fatalf("qr.png after wrong PIN: code=%d", qrRec.Code)
	}
}

func TestVerifyCorrectPINUnlocksQRWithExactPayload(t *testing.T) {
	srv, pin := newTestServer(t)
	if err := pin.Setup([]byte("4242")); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"pin": {"4242"}}
	req := httptest.NewRequest("POST", "/"+srv.session+"/verify", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "qr.png") {
		t.Fatalf("correct PIN verify: code=%d body=%s", rec.Code, rec.Body.String())
	}

	qrReq := httptest.NewRequest("GET", "/"+srv.session+"/qr.png", nil)
	qrRec := httptest.NewRecorder()
	srv.mux().ServeHTTP(qrRec, qrReq)
	if qrRec.Code != 200 || qrRec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("qr.png after correct PIN: code=%d content-type=%s", qrRec.Code, qrRec.Header().Get("Content-Type"))
	}
	if qrRec.Body.Len() < 8 || string(qrRec.Body.Bytes()[1:4]) != "PNG" {
		t.Fatalf("qr.png body does not look like a PNG (len=%d)", qrRec.Body.Len())
	}
}

func TestSetupRejectsWhenAlreadyConfigured(t *testing.T) {
	srv, pin := newTestServer(t)
	if err := pin.Setup([]byte("4242")); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"pin": {"9999"}}
	req := httptest.NewRequest("POST", "/"+srv.session+"/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux().ServeHTTP(rec, req)
	if rec.Code != 409 {
		t.Fatalf("setup when already configured: code=%d", rec.Code)
	}
}

func TestSetupConfiguresPINAndUnlocksQR(t *testing.T) {
	srv, pin := newTestServer(t)
	form := url.Values{"pin": {"1357"}}
	req := httptest.NewRequest("POST", "/"+srv.session+"/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "qr.png") {
		t.Fatalf("setup: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if configured, err := pin.IsConfigured(); err != nil || !configured {
		t.Fatalf("configured=%v err=%v after setup", configured, err)
	}
	if ok, _, err := pin.Verify([]byte("1357")); err != nil || !ok {
		t.Fatalf("newly-configured PIN does not verify: ok=%v err=%v", ok, err)
	}
}

func TestUnknownSessionPathIs404(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/wrong-session/", nil)
	rec := httptest.NewRecorder()
	srv.mux().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("unknown session path: code=%d", rec.Code)
	}
}

func TestQRPayloadMatchesAndroidExpectedShape(t *testing.T) {
	srv, pin := newTestServer(t)
	if err := pin.Setup([]byte("4242")); err != nil {
		t.Fatal(err)
	}
	srv.markVerified()
	raw, err := srv.payload.qrJSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"address", "host_public_key", "token"} {
		if decoded[field] == "" {
			t.Fatalf("qr payload missing required field %q: %s", field, raw)
		}
	}
	if _, hasExpiry := decoded["expires_at"]; hasExpiry {
		t.Fatal("qr payload must not include expires_at - Android client does not expect it")
	}
}

func TestShouldShutdownTriggers(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		now        time.Time
		expiresAt  time.Time
		lastActive time.Time
		qrServedAt time.Time
		want       bool
	}{
		{"nothing triggered yet", base, base.Add(time.Hour), base, time.Time{}, false},
		{"token expired", base.Add(time.Hour + time.Second), base.Add(time.Hour), base, time.Time{}, true},
		{"idle timeout", base.Add(idleTimeout + time.Second), time.Time{}, base, time.Time{}, true},
		{"grace period after QR elapsed", base.Add(graceAfterQR + time.Second), time.Time{}, base, base, true},
		{"within grace period after QR", base.Add(time.Second), time.Time{}, base, base, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := shouldShutdown(c.now, c.expiresAt, c.lastActive, idleTimeout, c.qrServedAt, graceAfterQR)
			if got != c.want {
				t.Fatalf("shouldShutdown=%v, want %v", got, c.want)
			}
		})
	}
}
