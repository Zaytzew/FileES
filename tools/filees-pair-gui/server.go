package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"net/http"
	"sync"
	"time"

	"filees/pkg/localpin"

	qrcode "github.com/skip2/go-qrcode"
)

// newSessionPath generates a random 128-bit hex path segment, so a
// co-resident local process/user cannot simply port-scan 127.0.0.1 and hit
// the pairing page without also knowing the URL this process itself opens
// in the system browser.
func newSessionPath() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// server is the loopback-only HTTP handler: a PIN gate in front of the QR
// code. It never listens on anything but 127.0.0.1, and the QR route is
// unreachable until Verify (or first-time Setup) succeeds.
type server struct {
	pin     *localpin.Store
	payload handoff
	session string

	mu       sync.Mutex
	verified bool
	qrPNG    []byte

	// onActivity/onQRServed let Run's shutdown watchdog react to requests
	// without the HTTP handler needing to know about process lifetime.
	onActivity func()
	onQRServed func()
}

func newServer(pin *localpin.Store, payload handoff, session string, onActivity, onQRServed func()) *server {
	if onActivity == nil {
		onActivity = func() {}
	}
	if onQRServed == nil {
		onQRServed = func() {}
	}
	return &server{pin: pin, payload: payload, session: session, onActivity: onActivity, onQRServed: onQRServed}
}

func (s *server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	base := "/" + s.session
	mux.HandleFunc(base+"/", s.handleIndex)
	mux.HandleFunc(base+"/setup", s.handleSetup)
	mux.HandleFunc(base+"/verify", s.handleVerify)
	mux.HandleFunc(base+"/qr.png", s.handleQR)
	return mux
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.onActivity()
	configured, err := s.pin.IsConfigured()
	if err != nil {
		http.Error(w, "local PIN store error", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	verified := s.verified
	s.mu.Unlock()
	if verified {
		writeQRPage(w, s.session)
		return
	}
	if !configured {
		writeFormPage(w, "Ustaw PIN", "Ustaw PIN, aby chronić dostęp do generatora parowania:", s.session+"/setup")
		return
	}
	writeFormPage(w, "Podaj PIN", "Podaj PIN, aby wygenerować kod parowania:", s.session+"/verify")
}

func (s *server) handleSetup(w http.ResponseWriter, r *http.Request) {
	s.onActivity()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if configured, err := s.pin.IsConfigured(); err != nil || configured {
		http.Error(w, "PIN already configured", http.StatusConflict)
		return
	}
	pin := []byte(r.FormValue("pin"))
	defer clear(pin)
	if len(pin) == 0 {
		writeFormPage(w, "Ustaw PIN", "PIN nie może być pusty. Spróbuj ponownie:", s.session+"/setup")
		return
	}
	if err := s.pin.Setup(pin); err != nil {
		http.Error(w, "nie udało się zapisać PIN-u", http.StatusInternalServerError)
		return
	}
	s.markVerified()
	writeQRPage(w, s.session)
}

func (s *server) handleVerify(w http.ResponseWriter, r *http.Request) {
	s.onActivity()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pin := []byte(r.FormValue("pin"))
	defer clear(pin)
	ok, locked, err := s.pin.Verify(pin)
	if err != nil {
		http.Error(w, "błąd weryfikacji PIN-u", http.StatusInternalServerError)
		return
	}
	if locked {
		http.Error(w, "PIN zablokowany po zbyt wielu błędnych próbach", http.StatusForbidden)
		return
	}
	if !ok {
		writeFormPage(w, "Podaj PIN", "Nieprawidłowy PIN. Spróbuj ponownie:", s.session+"/verify")
		return
	}
	s.markVerified()
	writeQRPage(w, s.session)
}

func (s *server) markVerified() {
	s.mu.Lock()
	s.verified = true
	s.mu.Unlock()
}

func (s *server) handleQR(w http.ResponseWriter, r *http.Request) {
	s.onActivity()
	s.mu.Lock()
	verified := s.verified
	png := s.qrPNG
	s.mu.Unlock()
	if !verified {
		http.Error(w, "PIN not verified", http.StatusForbidden)
		return
	}
	if png == nil {
		payloadJSON, err := s.payload.qrJSON()
		if err != nil {
			http.Error(w, "invalid pairing payload", http.StatusInternalServerError)
			return
		}
		built, err := qrcode.Encode(string(payloadJSON), qrcode.Medium, 320)
		if err != nil {
			http.Error(w, "failed to render QR code", http.StatusInternalServerError)
			return
		}
		s.mu.Lock()
		s.qrPNG = built
		png = built
		s.mu.Unlock()
	}
	s.onQRServed()
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}

func writeFormPage(w http.ResponseWriter, title, text, action string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>%s</title></head>
<body style="font-family:sans-serif;max-width:28rem;margin:3rem auto;text-align:center">
<h1>%s</h1><p>%s</p>
<form method="POST" action="/%s">
<input type="password" name="pin" autofocus style="font-size:1.5rem;text-align:center;letter-spacing:0.3rem">
<br><br><button type="submit" style="font-size:1.1rem;padding:0.5rem 1.5rem">OK</button>
</form></body></html>`, html.EscapeString(title), html.EscapeString(title), html.EscapeString(text), action)
}

func writeQRPage(w http.ResponseWriter, session string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Sparuj urządzenie</title></head>
<body style="font-family:sans-serif;max-width:28rem;margin:3rem auto;text-align:center">
<h1>Zeskanuj kod w aplikacji mobilnej</h1>
<img src="/%s/qr.png" alt="QR" style="width:320px;height:320px">
</body></html>`, session)
}

// shouldShutdown is the pure decision function behind Run's watchdog -
// extracted so the three independent shutdown triggers (idle, token
// expiry, post-QR grace period) are unit-testable without real timers.
func shouldShutdown(now, expiresAt time.Time, lastActivity time.Time, idleTimeout time.Duration, qrServedAt time.Time, graceAfterQR time.Duration) (shutdown bool, reason string) {
	if !expiresAt.IsZero() && !now.Before(expiresAt) {
		return true, "pairing token expired"
	}
	if !qrServedAt.IsZero() && !now.Before(qrServedAt.Add(graceAfterQR)) {
		return true, "QR code shown, grace period elapsed"
	}
	if !now.Before(lastActivity.Add(idleTimeout)) {
		return true, "idle timeout"
	}
	return false, ""
}
