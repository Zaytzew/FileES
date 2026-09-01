// Package reservationclient carries one reservation-projection request over
// the installation's pinned SSH identity, to the remote FileES server's
// filees-serving-state worker (internal/servertool/client_entry.go's
// ClientReservationCommand). One SSH exec session answers one request —
// mirrors pkg/controlclient's shape, deliberately kept as its own smaller
// protocol/transport pair rather than reusing control.Ticket, since this is
// a read with no durable, retryable mutation semantics. See
// concepts/RESERVATION_SERVER_EMISSION_WORKPLAN.md §"Granica procesu".
//
// This package performs exactly one call; it has no scheduling, coalescing
// or retry policy of its own — that is internal/gui/projectionrefresh's
// job (or whatever ultimately supplies its RefreshFunc), not this one.
package reservationclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	reservationv1 "filees/pkg/reservation/v1"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	Command     = "filees reservation-v1"
	ServiceUser = "_filees-client"
	// MaxResponseBytes matches pkg/reservationprojection's own artifact
	// read limit (io.LimitReader(f, 8<<20) in Store.load): a worker result
	// is essentially one artifact re-serialized, so a smaller limit here
	// would reject legitimate responses the store itself would happily
	// have written.
	MaxResponseBytes = 8 << 20
)

type Config struct {
	Address, IdentityFile, KnownHosts string
	Port                              int
	Timeout                           time.Duration
}

type Client struct {
	address  string
	timeout  time.Duration
	signer   ssh.Signer
	hostKeys ssh.HostKeyCallback
}

func New(config Config) (*Client, error) {
	if !filepath.IsAbs(config.IdentityFile) || !filepath.IsAbs(config.KnownHosts) {
		return nil, errors.New("reservation client identity and known_hosts must be absolute")
	}
	if config.Port < 1 || config.Port > 65535 {
		return nil, errors.New("reservation client SSH port is invalid")
	}
	host := strings.TrimSpace(config.Address)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if host == "" || strings.ContainsAny(host, "\x00\r\n\t ") {
		return nil, errors.New("reservation client server address is invalid")
	}
	privateKey, err := os.ReadFile(config.IdentityFile)
	if err != nil {
		return nil, fmt.Errorf("read reservation identity: %w", err)
	}
	defer clear(privateKey)
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, errors.New("reservation identity must be Ed25519")
	}
	hostKeys, err := knownhosts.New(config.KnownHosts)
	if err != nil {
		return nil, fmt.Errorf("load reservation host pin: %w", err)
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{address: net.JoinHostPort(host, strconv.Itoa(config.Port)), timeout: timeout, signer: signer, hostKeys: hostKeys}, nil
}

// Fetch asks the remote serving-state worker for repoID's current
// reservation projection. The returned Result may be Stale or Unknown; the
// caller decides what to do with each classification (see
// reservationv1.Result's doc comment) — Fetch itself never retries and
// never invents a fresher answer than the one the worker actually gave.
func (c *Client) Fetch(ctx context.Context, repoID string) (reservationv1.Result, error) {
	req := reservationv1.Request{Schema: reservationv1.Schema, RepoID: repoID}
	if err := req.Validate(); err != nil {
		return reservationv1.Result{}, err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return reservationv1.Result{}, err
	}
	connection, err := (&net.Dialer{Timeout: c.timeout}).DialContext(ctx, "tcp", c.address)
	if err != nil {
		return reservationv1.Result{}, fmt.Errorf("connect reservation worker: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(c.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return reservationv1.Result{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	sshConfig := &ssh.ClientConfig{User: ServiceUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(c.signer)}, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, HostKeyCallback: c.hostKeys, Timeout: c.timeout}
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, c.address, sshConfig)
	if err != nil {
		return reservationv1.Result{}, fmt.Errorf("reservation worker SSH handshake: %w", err)
	}
	sshClient := ssh.NewClient(clientConnection, channels, requests)
	defer sshClient.Close()
	session, err := sshClient.NewSession()
	if err != nil {
		return reservationv1.Result{}, err
	}
	defer session.Close()
	session.Stdin = bytes.NewReader(append(raw, '\n'))
	stdout, err := session.StdoutPipe()
	if err != nil {
		return reservationv1.Result{}, err
	}
	var stderr limitedBuffer
	session.Stderr = &stderr
	if err := session.Start(Command); err != nil {
		return reservationv1.Result{}, fmt.Errorf("start reservation worker: %w", err)
	}
	response, readErr := io.ReadAll(io.LimitReader(stdout, MaxResponseBytes+1))
	waitErr := session.Wait()
	if waitErr != nil {
		return reservationv1.Result{}, fmt.Errorf("reservation worker failed: %w: %s", waitErr, stderr.String())
	}
	if readErr != nil {
		return reservationv1.Result{}, fmt.Errorf("read reservation worker result: %w", readErr)
	}
	if len(response) > MaxResponseBytes {
		return reservationv1.Result{}, errors.New("reservation worker result exceeds limit")
	}
	result, err := reservationv1.ParseResult(bytes.TrimSpace(response))
	if err != nil {
		return reservationv1.Result{}, fmt.Errorf("parse reservation worker result: %w", err)
	}
	if result.RepoID != repoID {
		return reservationv1.Result{}, errors.New("reservation worker result does not match request")
	}
	return result, nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(raw []byte) (int, error) {
	original := len(raw)
	remaining := MaxResponseBytes - b.Len()
	if remaining > 0 {
		if len(raw) > remaining {
			raw = raw[:remaining]
		}
		_, _ = b.Buffer.Write(raw)
	}
	return original, nil
}
