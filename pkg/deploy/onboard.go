package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/onboarding"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const OnboardServerCommand = "filees onboard-v1"

// SubmitOnboarding performs the only SSH operation for which the compiled
// bootstrap key is valid. The server address, account and command are client
// policy; caller-controlled data exists only inside the versioned stdin frame.
func SubmitOnboarding(ctx context.Context, knownHostsPath, email, requestID string) (onboarding.OnboardResponse, error) {
	return submitOnboarding(ctx, BootstrapHost+":22", knownHostsPath, email, requestID)
}

func submitOnboarding(ctx context.Context, address, knownHostsPath, email, requestID string) (onboarding.OnboardResponse, error) {
	knownHostsPath = filepath.Clean(strings.TrimSpace(knownHostsPath))
	if knownHostsPath == "." || !filepath.IsAbs(knownHostsPath) {
		return onboarding.OnboardResponse{}, errors.New("pinned known_hosts path must be absolute")
	}
	canonical, err := onboarding.CanonicalEmail(email)
	if err != nil {
		return onboarding.OnboardResponse{}, err
	}
	if _, err := uuid.Parse(requestID); err != nil {
		return onboarding.OnboardResponse{}, errors.New("onboarding_request_id must be a UUID")
	}
	signer, err := BootstrapSigner()
	if err != nil {
		return onboarding.OnboardResponse{}, err
	}
	hostKey, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return onboarding.OnboardResponse{}, fmt.Errorf("load pinned host key: %w", err)
	}
	config := &ssh.ClientConfig{
		User:              OnboardUser,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   hostKey,
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           15 * time.Second,
	}
	connection, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return onboarding.OnboardResponse{}, fmt.Errorf("bootstrap SSH connect: %w", err)
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, address, config)
	if err != nil {
		return onboarding.OnboardResponse{}, fmt.Errorf("bootstrap SSH handshake: %w", err)
	}
	client := ssh.NewClient(clientConnection, channels, requests)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return onboarding.OnboardResponse{}, err
	}
	defer session.Close()
	request := onboarding.OnboardRequest{Schema: onboarding.OnboardRequestSchema, Email: canonical, OnboardingRequestID: requestID}
	raw, _ := json.Marshal(request)
	session.Stdin = bytes.NewReader(append(raw, '\n'))
	output, err := session.Output(OnboardServerCommand)
	if err != nil {
		return onboarding.OnboardResponse{}, fmt.Errorf("bootstrap SSH command: %w", err)
	}
	if len(output) > 16*1024 {
		return onboarding.OnboardResponse{}, errors.New("bootstrap SSH response exceeds 16 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var response onboarding.OnboardResponse
	if err := decoder.Decode(&response); err != nil {
		return onboarding.OnboardResponse{}, fmt.Errorf("decode bootstrap response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return onboarding.OnboardResponse{}, errors.New("bootstrap response contains trailing JSON")
	}
	if response.Schema != onboarding.OnboardResponseSchema || response.Status != "accepted" || response.OnboardingRequestID != requestID {
		return onboarding.OnboardResponse{}, errors.New("bootstrap response does not match request")
	}
	return response, nil
}
