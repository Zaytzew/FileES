// Package controlclient carries repository lifecycle tickets over the
// installation's pinned SSH identity. One SSH exec session carries one ticket.
package controlclient

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

	control "filees/pkg/control/v1"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	Command          = "filees control-v1"
	ServiceUser      = "_filees-client"
	MaxResponseBytes = 64 << 10
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
		return nil, errors.New("control client identity and known_hosts must be absolute")
	}
	if config.Port < 1 || config.Port > 65535 {
		return nil, errors.New("control client SSH port is invalid")
	}
	host := strings.TrimSpace(config.Address)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if host == "" || strings.ContainsAny(host, "\x00\r\n\t ") {
		return nil, errors.New("control client server address is invalid")
	}
	privateKey, err := os.ReadFile(config.IdentityFile)
	if err != nil {
		return nil, fmt.Errorf("read control identity: %w", err)
	}
	defer clear(privateKey)
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, errors.New("control identity must be Ed25519")
	}
	hostKeys, err := knownhosts.New(config.KnownHosts)
	if err != nil {
		return nil, fmt.Errorf("load control host pin: %w", err)
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{address: net.JoinHostPort(host, strconv.Itoa(config.Port)), timeout: timeout, signer: signer, hostKeys: hostKeys}, nil
}

func (c *Client) Exchange(ctx context.Context, ticket control.Ticket) (control.Result, error) {
	if err := ticket.Validate(); err != nil {
		return control.Result{}, err
	}
	raw, err := json.Marshal(ticket)
	if err != nil {
		return control.Result{}, err
	}
	connection, err := (&net.Dialer{Timeout: c.timeout}).DialContext(ctx, "tcp", c.address)
	if err != nil {
		return control.Result{}, fmt.Errorf("connect repository control: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(c.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return control.Result{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	sshConfig := &ssh.ClientConfig{User: ServiceUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(c.signer)}, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, HostKeyCallback: c.hostKeys, Timeout: c.timeout}
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, c.address, sshConfig)
	if err != nil {
		return control.Result{}, fmt.Errorf("repository control SSH handshake: %w", err)
	}
	sshClient := ssh.NewClient(clientConnection, channels, requests)
	defer sshClient.Close()
	session, err := sshClient.NewSession()
	if err != nil {
		return control.Result{}, err
	}
	defer session.Close()
	session.Stdin = bytes.NewReader(append(raw, '\n'))
	stdout, err := session.StdoutPipe()
	if err != nil {
		return control.Result{}, err
	}
	var stderr limitedBuffer
	session.Stderr = &stderr
	if err := session.Start(Command); err != nil {
		return control.Result{}, fmt.Errorf("start repository control: %w", err)
	}
	response, readErr := io.ReadAll(io.LimitReader(stdout, MaxResponseBytes+1))
	waitErr := session.Wait()
	if waitErr != nil {
		return control.Result{}, fmt.Errorf("repository control failed: %w: %s", waitErr, stderr.String())
	}
	if readErr != nil {
		return control.Result{}, fmt.Errorf("read repository control result: %w", readErr)
	}
	if len(response) > MaxResponseBytes {
		return control.Result{}, errors.New("repository control result exceeds limit")
	}
	result, err := control.ParseResult(bytes.TrimSpace(response))
	if err != nil {
		return control.Result{}, fmt.Errorf("parse repository control result: %w", err)
	}
	if result.OperationID != ticket.OperationID || result.RequestID != ticket.RequestID || result.Type != ticket.Type {
		return control.Result{}, errors.New("repository control result does not match ticket")
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
