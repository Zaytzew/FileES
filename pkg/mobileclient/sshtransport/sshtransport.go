// Package sshtransport is the mobileclient.Transport used on real devices: it
// dials one embedded SSH connection per operation (golang.org/x/crypto/ssh,
// not a system ssh binary — Android has none) and carries exactly one framed
// mobile operation over a single exec session, then closes everything. This
// mirrors internal/mobileworker.Dispatcher's "one operation per session"
// contract on the server side and FILEES_ANDROID_CLIENT_CONCEPT_V2.md §4.3.
package sshtransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	v1 "filees/pkg/mobile/v1"

	"golang.org/x/crypto/ssh"
)

// Command is the exec payload sent when opening the session. The server's
// forced command (filees-mobile-v1) always overrides whatever the client
// asks for, per the SSH class design in §4.3; this name only documents intent
// and gives a real command a human-readable label in server-side logs before
// the override takes effect.
const Command = "filees-mobile-v1"

const defaultDialTimeout = 10 * time.Second

// Config pins everything needed to reach one FileES mobile endpoint. There is
// no host-key-on-first-use here: HostPublicKey must be pinned out of band
// (the same activation flow that binds the desktop client to a server).
type Config struct {
	Address       string // host:port
	User          string // technical account bound to the filees-mobile-v1 login class
	HostPublicKey string // pinned bare "ssh-ed25519 AAAA..." authorized_keys line
	Signer        ssh.Signer
	DialTimeout   time.Duration // defaults to 10s
}

// Transport dials a fresh SSH connection for every operation.
type Transport struct {
	cfg     Config
	hostKey ssh.PublicKey
}

// New validates cfg and returns a ready Transport.
func New(cfg Config) (*Transport, error) {
	if strings.TrimSpace(cfg.Address) == "" {
		return nil, errors.New("sshtransport: address is required")
	}
	if strings.TrimSpace(cfg.User) == "" {
		return nil, errors.New("sshtransport: user is required")
	}
	if cfg.Signer == nil {
		return nil, errors.New("sshtransport: signer is required")
	}
	hostKey, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(cfg.HostPublicKey)))
	if err != nil || hostKey.Type() != ssh.KeyAlgoED25519 || comment != "" || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("sshtransport: host_public_key must be one bare ssh-ed25519 key")
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = defaultDialTimeout
	}
	return &Transport{cfg: cfg, hostKey: hostKey}, nil
}

// Do implements mobileclient.Transport: one connection, one exec session, one
// framed request out and one framed response back.
func (t *Transport) Do(ctx context.Context, req v1.Request, reqPayload []byte) (v1.Response, []byte, error) {
	header, err := json.Marshal(req)
	if err != nil {
		return v1.Response{}, nil, fmt.Errorf("sshtransport: encode request: %w", err)
	}

	config := &ssh.ClientConfig{
		User:              t.cfg.User,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(t.cfg.Signer)},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if !keysEqual(key, t.hostKey) {
				return errors.New("sshtransport: host key mismatch")
			}
			return nil
		},
		Timeout: t.cfg.DialTimeout,
	}

	connection, err := (&net.Dialer{Timeout: t.cfg.DialTimeout}).DialContext(ctx, "tcp", t.cfg.Address)
	if err != nil {
		return v1.Response{}, nil, fmt.Errorf("sshtransport: dial: %w", err)
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return v1.Response{}, nil, fmt.Errorf("sshtransport: set deadline: %w", err)
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()

	clientConn, channels, requests, err := ssh.NewClientConn(connection, t.cfg.Address, config)
	if err != nil {
		return v1.Response{}, nil, fmt.Errorf("sshtransport: handshake: %w", err)
	}
	client := ssh.NewClient(clientConn, channels, requests)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return v1.Response{}, nil, fmt.Errorf("sshtransport: open session: %w", err)
	}
	defer session.Close()

	var stdin bytes.Buffer
	if err := v1.WriteFrame(&stdin, v1.RequestMagic, header, reqPayload); err != nil {
		return v1.Response{}, nil, fmt.Errorf("sshtransport: frame request: %w", err)
	}
	session.Stdin = &stdin

	stdout, err := session.StdoutPipe()
	if err != nil {
		return v1.Response{}, nil, fmt.Errorf("sshtransport: stdout pipe: %w", err)
	}

	if err := session.Start(Command); err != nil {
		return v1.Response{}, nil, fmt.Errorf("sshtransport: start session: %w", err)
	}

	// Read the full response before Wait, matching pkg/deploy/session.go's
	// callHelper: an over-long or hung remote must not be able to block Wait
	// while we are still trying to drain its output.
	respHeader, respPayload, readErr := v1.ReadFrame(stdout, v1.ResponseMagic, v1.MaxHeaderBytes)

	if waitErr := session.Wait(); waitErr != nil {
		if readErr != nil {
			return v1.Response{}, nil, fmt.Errorf("sshtransport: session failed: %w (response read: %v)", waitErr, readErr)
		}
		return v1.Response{}, nil, fmt.Errorf("sshtransport: session failed: %w", waitErr)
	}
	if readErr != nil {
		return v1.Response{}, nil, fmt.Errorf("sshtransport: read response: %w", readErr)
	}

	resp, err := v1.ParseResponse(respHeader)
	if err != nil {
		return v1.Response{}, nil, fmt.Errorf("sshtransport: parse response: %w", err)
	}
	return resp, respPayload, nil
}

func keysEqual(a, b ssh.PublicKey) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Type() == b.Type() && bytes.Equal(a.Marshal(), b.Marshal())
}
