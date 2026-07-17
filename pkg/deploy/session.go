package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const TunnelSessionSchema = "filees.tunnel-session/v2"

const helperResponseLimit = 64 * 1024

type TunnelSession struct {
	Schema              string `json:"schema"`
	DeployRequestID     string `json:"deploy_request_id"`
	HelperHostPublicKey string `json:"helper_host_public_key"`
	ReconnectPublicKey  string `json:"reconnect_public_key"`
}

func (s TunnelSession) Validate() error {
	if s.Schema != TunnelSessionSchema {
		return fmt.Errorf("unsupported tunnel session schema %q", s.Schema)
	}
	if _, err := uuid.Parse(s.DeployRequestID); err != nil {
		return errors.New("deploy_request_id must be a UUID")
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(s.HelperHostPublicKey)))
	if err != nil || key.Type() != ssh.KeyAlgoED25519 || comment != "" || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return errors.New("helper_host_public_key must be one bare ssh-ed25519 key")
	}
	key, comment, options, rest, err = ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(s.ReconnectPublicKey)))
	if err != nil || key.Type() != ssh.KeyAlgoED25519 || comment != "" || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return errors.New("reconnect_public_key must be one bare ssh-ed25519 key")
	}
	return nil
}

func DecodeTunnelSession(reader io.Reader) (TunnelSession, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 16*1024))
	decoder.DisallowUnknownFields()
	var session TunnelSession
	if err := decoder.Decode(&session); err != nil {
		return TunnelSession{}, fmt.Errorf("decode tunnel session: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return TunnelSession{}, errors.New("decode tunnel session: trailing JSON value")
	}
	if err := session.Validate(); err != nil {
		return TunnelSession{}, err
	}
	return session, nil
}

func EncodeTunnelSession(session TunnelSession) ([]byte, error) {
	if err := session.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(session)
	return append(raw, '\n'), err
}

// GenerateIdentityThroughHelper performs the sole S3 helper action through
// the already established loopback reverse forward.
func GenerateIdentityThroughHelper(ctx context.Context, address, hostPublicKey string, signer ssh.Signer, request HelperRequest) (HelperResponse, error) {
	response, err := callHelper(ctx, address, hostPublicKey, signer, request)
	if err != nil {
		return HelperResponse{}, err
	}
	if response.Identity == nil {
		return HelperResponse{}, errors.New("helper identity response is missing")
	}
	return response, nil
}

func ProveServiceAccessThroughHelper(ctx context.Context, address, hostPublicKey string, signer ssh.Signer, request HelperRequest) error {
	response, err := callHelper(ctx, address, hostPublicKey, signer, request)
	if err != nil {
		return err
	}
	if response.Identity != nil {
		return errors.New("helper proof response contains identity material")
	}
	return nil
}

func callHelper(ctx context.Context, address, hostPublicKey string, signer ssh.Signer, request HelperRequest) (HelperResponse, error) {
	if signer == nil {
		return HelperResponse{}, errors.New("worker signer is required")
	}
	if err := request.validate(request.OperationID, request.ClientID); err != nil {
		return HelperResponse{}, err
	}
	hostKey, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(hostPublicKey)))
	if err != nil || hostKey.Type() != ssh.KeyAlgoED25519 || comment != "" || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return HelperResponse{}, errors.New("invalid pinned helper host key")
	}
	config := &ssh.ClientConfig{
		User: HelperUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if !keysEqual(key, hostKey) {
				return errors.New("helper host key mismatch")
			}
			return nil
		},
		Timeout: 2 * time.Second,
	}
	connection, err := dialHelperWithRetry(ctx, address)
	if err != nil {
		return HelperResponse{}, err
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return HelperResponse{}, fmt.Errorf("set helper connection deadline: %w", err)
		}
	}
	stopContextClose := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopContextClose()
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, address, config)
	if err != nil {
		return HelperResponse{}, fmt.Errorf("helper SSH handshake: %w", err)
	}
	client := ssh.NewClient(clientConnection, channels, requests)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return HelperResponse{}, err
	}
	defer session.Close()
	raw, _ := json.Marshal(request)
	session.Stdin = bytes.NewReader(append(raw, '\n'))
	stdout, err := session.StdoutPipe()
	if err != nil {
		return HelperResponse{}, fmt.Errorf("helper stdout: %w", err)
	}
	if err := session.Start(HelperCommand); err != nil {
		return HelperResponse{}, fmt.Errorf("start helper action: %w", err)
	}
	output, err := io.ReadAll(io.LimitReader(stdout, helperResponseLimit+1))
	if err != nil {
		return HelperResponse{}, fmt.Errorf("read helper response: %w", err)
	}
	if len(output) > helperResponseLimit {
		// Do not Wait after stopping stdout consumption: a hostile helper could
		// fill the SSH channel window and keep Wait blocked. Closing the session
		// makes the over-limit path bounded independently of remote behaviour.
		_ = session.Close()
		return HelperResponse{}, errors.New("helper response exceeds 64 KiB")
	}
	if err := session.Wait(); err != nil {
		return HelperResponse{}, fmt.Errorf("helper action: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var response HelperResponse
	if err := decoder.Decode(&response); err != nil {
		return HelperResponse{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return HelperResponse{}, errors.New("helper response contains trailing JSON")
	}
	if response.Schema != HelperSchema || response.Status != "ok" || response.OperationID != request.OperationID || response.RequestID != request.RequestID || response.Error != "" {
		return HelperResponse{}, errors.New("helper response does not match request")
	}
	return response, nil
}

func dialHelperWithRetry(ctx context.Context, address string) (net.Conn, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		connection, err := (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext(ctx, "tcp", address)
		if err == nil {
			return connection, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("helper reverse forward unavailable: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
