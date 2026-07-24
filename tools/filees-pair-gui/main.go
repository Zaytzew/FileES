// Command filees-pair-gui is the standalone helper spawned by the desktop
// tray (cmd/filees-gui) to render a mobile-pairing QR code behind a local
// PIN gate. It is deliberately almost a separate application: its own Go
// module (see go.mod's replace directive), its own dependency (QR
// encoding), and its own process - the bulk of this feature's logic lives
// here specifically to keep it out of cmd/filees-gui/internal/gui beyond a
// few thin integration points (tray intent, IPC command, PIN setup at
// activation).
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"filees/pkg/localpin"
)

var version = "dev"

const (
	idleTimeout  = 2 * time.Minute
	graceAfterQR = 30 * time.Second
	watchdogTick = time.Second
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("filees-pair-gui: %v", err)
	}
}

func run() error {
	log.Printf("filees-pair-gui: version=%s", version)
	payload, err := readHandoff(os.Stdin)
	if err != nil {
		return fmt.Errorf("read pairing handoff: %w", err)
	}
	expiresAt, _ := payload.expiry() // zero Time if unparsable - treated as "no expiry trigger"

	pin, err := localpin.Open(localpin.DefaultRoot())
	if err != nil {
		return fmt.Errorf("open local PIN store: %w", err)
	}

	session, err := newSessionPath()
	if err != nil {
		return fmt.Errorf("generate session path: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("bind loopback listener: %w", err)
	}

	activity := make(chan time.Time, 1)
	qrServed := make(chan time.Time, 1)
	notifyActivity := func() {
		select {
		case activity <- time.Now():
		default:
		}
	}
	notifyQRServed := func() {
		select {
		case qrServed <- time.Now():
		default:
		}
	}

	srv := newServer(pin, payload, session, notifyActivity, notifyQRServed)
	httpServer := &http.Server{Handler: srv.mux()}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()

	url := fmt.Sprintf("http://%s/%s/", listener.Addr().String(), session)
	if err := openBrowser(url); err != nil {
		log.Printf("filees-pair-gui: could not open browser automatically, open manually: %s (%v)", url, err)
	}

	watchdog(httpServer, expiresAt, activity, qrServed)
	if err := <-serveErr; err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// watchdog blocks until one of the three shutdown triggers fires (idle
// timeout, pairing-token expiry, or the grace period after the QR was
// first shown), then shuts the HTTP server down.
func watchdog(httpServer *http.Server, expiresAt time.Time, activity, qrServed <-chan time.Time) {
	lastActivity := time.Now()
	var qrServedAt time.Time
	ticker := time.NewTicker(watchdogTick)
	defer ticker.Stop()
	for {
		select {
		case t := <-activity:
			lastActivity = t
		case t := <-qrServed:
			if qrServedAt.IsZero() {
				qrServedAt = t
			}
		case now := <-ticker.C:
			if stop, reason := shouldShutdown(now, expiresAt, lastActivity, idleTimeout, qrServedAt, graceAfterQR); stop {
				log.Printf("filees-pair-gui: shutting down (%s)", reason)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = httpServer.Shutdown(ctx)
				return
			}
		}
	}
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
