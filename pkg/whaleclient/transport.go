// Package whaleclient carries Whale windows over the desktop client's pinned
// SSH identity. One SSH exec session carries exactly one Whale operation.
package whaleclient

import (
	"bufio"
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

	whale "filees/pkg/whale/v1"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	Command     = "filees whale-v1"
	ServiceUser = "_filees-client"
)

type Config struct {
	Address, IdentityFile, KnownHosts string
	Port                              int
	Timeout                           time.Duration
}

type Transport struct {
	address  string
	timeout  time.Duration
	signer   ssh.Signer
	hostKeys ssh.HostKeyCallback
}

func NewTransport(config Config) (*Transport, error) {
	if !filepath.IsAbs(config.IdentityFile) || !filepath.IsAbs(config.KnownHosts) {
		return nil, errors.New("Whale client identity and known_hosts must be absolute")
	}
	if config.Port < 1 || config.Port > 65535 {
		return nil, errors.New("Whale client SSH port is invalid")
	}
	host := strings.TrimSpace(config.Address)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if host == "" || strings.ContainsAny(host, "\x00\r\n\t ") {
		return nil, errors.New("Whale client server address is invalid")
	}
	privateKey, err := os.ReadFile(config.IdentityFile)
	if err != nil {
		return nil, fmt.Errorf("read Whale identity: %w", err)
	}
	defer clear(privateKey)
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, errors.New("Whale identity must be Ed25519")
	}
	hostKeys, err := knownhosts.New(config.KnownHosts)
	if err != nil {
		return nil, fmt.Errorf("load Whale host pin: %w", err)
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return &Transport{address: net.JoinHostPort(host, strconv.Itoa(config.Port)), timeout: timeout, signer: signer, hostKeys: hostKeys}, nil
}

// Do exchanges one operation. PUT_WINDOW is deliberately two-phase: only the
// request header is written before the server confirms the durable offset;
// payload bytes are released after that acknowledgement. All other currently
// supported operations are header-only.
func (t *Transport) Do(ctx context.Context, request whale.Request, payload io.Reader, receive ...io.Writer) (whale.Response, error) {
	if err := request.Validate(); err != nil {
		return whale.Response{}, err
	}
	header, err := json.Marshal(request)
	if err != nil {
		return whale.Response{}, err
	}
	connection, err := (&net.Dialer{Timeout: t.timeout}).DialContext(ctx, "tcp", t.address)
	if err != nil {
		return whale.Response{}, fmt.Errorf("connect Whale endpoint: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(t.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return whale.Response{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()

	sshConfig := &ssh.ClientConfig{User: ServiceUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(t.signer)}, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, HostKeyCallback: t.hostKeys, Timeout: t.timeout}
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, t.address, sshConfig)
	if err != nil {
		return whale.Response{}, fmt.Errorf("Whale SSH handshake: %w", err)
	}
	sshClient := ssh.NewClient(clientConnection, channels, requests)
	defer sshClient.Close()
	session, err := sshClient.NewSession()
	if err != nil {
		return whale.Response{}, err
	}
	defer session.Close()
	stdin, err := session.StdinPipe()
	if err != nil {
		return whale.Response{}, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return whale.Response{}, err
	}
	var stderr limitedBuffer
	session.Stderr = &stderr
	if err := session.Start(Command); err != nil {
		return whale.Response{}, fmt.Errorf("start Whale session: %w", err)
	}
	if err := whale.WriteFrame(stdin, whale.RequestMagic, header); err != nil {
		_ = stdin.Close()
		_ = session.Wait()
		return whale.Response{}, fmt.Errorf("write Whale request: %w", err)
	}

	reader := bufio.NewReader(stdout)
	if request.Operation == whale.OpPutWindow {
		if len(receive) != 0 {
			_ = stdin.Close()
			_ = session.Wait()
			return whale.Response{}, errors.New("Whale PUT window cannot receive payload")
		}
		if payload == nil {
			_ = stdin.Close()
			_ = session.Wait()
			return whale.Response{}, errors.New("Whale PUT window requires payload")
		}
		ready, err := readResponse(reader, request)
		if err != nil {
			_ = stdin.Close()
			_ = session.Wait()
			return whale.Response{}, err
		}
		if ready.Status != "continue" || ready.Result.Offset != request.Offset {
			_ = stdin.Close()
			_ = session.Wait()
			return whale.Response{}, errors.New("Whale server did not confirm the requested durable offset")
		}
		written, copyErr := io.CopyN(stdin, payload, request.PayloadSize)
		if copyErr != nil {
			_ = stdin.Close()
			_ = session.Wait()
			return whale.Response{}, fmt.Errorf("send Whale window after %d of %d bytes: %w", written, request.PayloadSize, copyErr)
		}
	} else if payload != nil {
		_ = stdin.Close()
		_ = session.Wait()
		return whale.Response{}, errors.New("Whale control operation cannot carry payload")
	}
	if err := stdin.Close(); err != nil {
		_ = session.Wait()
		return whale.Response{}, err
	}
	response, readErr := readResponse(reader, request)
	if readErr == nil && request.Operation == whale.OpGetWindow {
		if len(receive) != 1 || receive[0] == nil {
			readErr = errors.New("Whale GET window requires one destination")
		} else if response.Result.Offset != request.Offset || response.Result.PayloadSize != request.PayloadSize {
			readErr = errors.New("Whale GET window does not match requested offset and size")
		} else {
			var written int64
			written, readErr = io.CopyN(receive[0], reader, response.Result.PayloadSize)
			if readErr != nil {
				readErr = fmt.Errorf("receive Whale window after %d of %d bytes: %w", written, response.Result.PayloadSize, readErr)
			}
		}
	} else if readErr == nil && len(receive) != 0 {
		readErr = errors.New("Whale control operation cannot receive payload")
	}
	waitErr := session.Wait()
	if waitErr != nil {
		return whale.Response{}, fmt.Errorf("Whale session failed: %w: %s", waitErr, stderr.String())
	}
	if readErr != nil {
		return whale.Response{}, readErr
	}
	return response, nil
}

func readResponse(reader *bufio.Reader, request whale.Request) (whale.Response, error) {
	header, err := whale.ReadHeader(reader, whale.ResponseMagic)
	if err != nil {
		return whale.Response{}, fmt.Errorf("read Whale response: %w", err)
	}
	response, err := whale.ParseResponse(header)
	if err != nil {
		return whale.Response{}, fmt.Errorf("parse Whale response: %w", err)
	}
	if response.RequestID != request.RequestID || response.Operation != request.Operation {
		return whale.Response{}, errors.New("Whale response does not match request")
	}
	if response.Status == "error" {
		return response, RemoteError{Body: *response.Error}
	}
	return response, nil
}

type RemoteError struct{ Body whale.ErrorBody }

func (e RemoteError) Error() string { return e.Body.Message }

const maxStderrBytes = 64 << 10

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(raw []byte) (int, error) {
	original := len(raw)
	remaining := maxStderrBytes - b.Len()
	if remaining > 0 {
		if len(raw) > remaining {
			raw = raw[:remaining]
		}
		_, _ = b.Buffer.Write(raw)
	}
	return original, nil
}
