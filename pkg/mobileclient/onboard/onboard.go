// Package onboard is the mobile client side of pairing with an already-active
// desktop installation (concepts/FILEES_ANDROID_CLIENT_CONCEPT_V2.md §4.2).
// There is no ticket, no e-mail, no reverse tunnel: the desktop mints a
// short-lived pairing token through its own control-plane channel and shows
// it (address + pinned host key + token) as a QR code; this package drives
// the three outbound-only SSH hops that follow, each modeled directly on
// pkg/mobileclient/sshtransport.Do's "one dial, one exec session" shape:
//
//  1. PushInstallationKey presents the token exactly where a desktop client
//     would present its mailed OTP (the same _filees-tunnel BSD-Auth class,
//     onboarding.Files.AuthenticateOTP, unchanged), then pushes the
//     already-locally-generated installation public key as a plain JSON
//     payload - a public key is not secret, so there is no reverse tunnel
//     to fetch it through.
//  2. ProveInstallationKey dials again with that same key (now staged and
//     authorized server-side) to prove possession.
//  3. FinishActivation dials once more to request activation, publishing the
//     service view and returning the resulting SVN revision.
//
// Neither (2) nor (3) send any payload: the server already knows which
// operation/client this is from the authorized_keys command= line bound to
// the connecting key itself (internal/servertool/mobile_entry.go), never
// from anything the client says.
package onboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const defaultDialTimeout = 10 * time.Second

// tunnelUser is the technical account bound to the _filees-tunnel BSD-Auth
// login class - the same one desktop's push-deploy reverse tunnel
// authenticates as (pkg/deploy.TunnelUser). Not imported from pkg/deploy
// directly: that package is desktop/exec("ssh")-specific end to end, and
// mobile has no other reason to depend on it.
const tunnelUser = "_filees-tunnel"

// pushCommand/proofCommand/finishCommand mirror the exact SSH_ORIGINAL_COMMAND
// strings internal/servertool/entry.go and mobile_entry.go dispatch on.
const (
	pushCommand   = "filees mobile-onboard-v1"
	proofCommand  = "filees-mobile-proof/v1"
	finishCommand = "filees-mobile-finish/v1"
)

// pushResult/proofResult mirror internal/servertool/mobile_worker.go's
// mobileWorkerResult wire shape (plain JSON, not the pkg/mobile/v1 frame
// format sshtransport uses for operational traffic).
type workerResult struct {
	Schema          string `json:"schema"`
	Status          string `json:"status"`
	OperationID     string `json:"operation_id,omitempty"`
	ClientID        string `json:"client_id,omitempty"`
	ServiceRevision int64  `json:"service_revision,omitempty"`
}

// pushPayload mirrors internal/servertool/mobile_worker.go's
// MobileOnboardPushPayload.
type pushPayload struct {
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
}

// PairingConfig pins the endpoint scanned from the desktop's QR code.
type PairingConfig struct {
	Address       string // host:port
	HostPublicKey string // pinned bare "ssh-ed25519 AAAA..." line, from the QR
	DialTimeout   time.Duration
}

func (cfg PairingConfig) validate() (ssh.PublicKey, time.Duration, error) {
	if strings.TrimSpace(cfg.Address) == "" {
		return nil, 0, errors.New("onboard: address is required")
	}
	hostKey, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(cfg.HostPublicKey)))
	if err != nil || hostKey.Type() != ssh.KeyAlgoED25519 || comment != "" || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, 0, errors.New("onboard: host_public_key must be one bare ssh-ed25519 key")
	}
	timeout := cfg.DialTimeout
	if timeout <= 0 {
		timeout = defaultDialTimeout
	}
	return hostKey, timeout, nil
}

// PushInstallationKey presents token over the _filees-tunnel BSD-Auth
// session and pushes publicKey/fingerprint. Returns the operation_id and
// client_id the server assigned to this pairing, needed by nothing further
// on the client side (ProveInstallationKey/FinishActivation need no
// arguments at all - the server already knows both from the key itself).
func PushInstallationKey(ctx context.Context, cfg PairingConfig, token, publicKey, fingerprint string) (operationID, clientID string, err error) {
	if strings.TrimSpace(token) == "" {
		return "", "", errors.New("onboard: pairing token is required")
	}
	if strings.TrimSpace(publicKey) == "" || strings.TrimSpace(fingerprint) == "" {
		return "", "", errors.New("onboard: public key and fingerprint are required")
	}
	hostKey, timeout, err := cfg.validate()
	if err != nil {
		return "", "", err
	}
	payload, err := json.Marshal(pushPayload{PublicKey: publicKey, Fingerprint: fingerprint})
	if err != nil {
		return "", "", fmt.Errorf("onboard: encode push payload: %w", err)
	}
	answered := false
	config := &ssh.ClientConfig{
		User: tunnelUser,
		Auth: []ssh.AuthMethod{ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			// The BSD Auth challenge text (if any) is deliberately ignored:
			// onboarding.Files.AuthenticateOTP verifies the token directly,
			// it does not need the challenge nonce echoed back (that
			// signed-response shape is reconnect-only, see
			// pkg/onboarding/reconnect.go, not used here). Answer every
			// question with the token - in practice there is exactly one.
			answered = true
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = token
			}
			return answers, nil
		})},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		HostKeyCallback:   pinnedHostKey(hostKey),
		Timeout:           timeout,
	}
	result, err := dialExecAndDecode(ctx, cfg.Address, timeout, config, pushCommand, payload)
	if err != nil {
		return "", "", err
	}
	if !answered {
		return "", "", errors.New("onboard: server never requested the pairing token")
	}
	if result.Status != "staged" || result.OperationID == "" || result.ClientID == "" {
		return "", "", fmt.Errorf("onboard: unexpected push result %+v", result)
	}
	return result.OperationID, result.ClientID, nil
}

// ProofConfig pins the endpoint and the freshly-generated installation
// identity used for the two post-push hops. Once Phase 3
// (packaging/server/openbsd/filees.conf's operational _filees-mobile class)
// lands, User will be that account's technical name - renderAccessLocked's
// restrict,command= already forces the right forced command regardless of
// which account is used to connect, exactly like desktop's
// _filees-client/_filees-data split.
type ProofConfig struct {
	Address       string
	User          string
	HostPublicKey string
	Signer        ssh.Signer
	DialTimeout   time.Duration
}

func (cfg ProofConfig) validate() (ssh.PublicKey, time.Duration, error) {
	pairing := PairingConfig{Address: cfg.Address, HostPublicKey: cfg.HostPublicKey, DialTimeout: cfg.DialTimeout}
	hostKey, timeout, err := pairing.validate()
	if err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(cfg.User) == "" {
		return nil, 0, errors.New("onboard: user is required")
	}
	if cfg.Signer == nil {
		return nil, 0, errors.New("onboard: signer is required")
	}
	return hostKey, timeout, nil
}

// ProveInstallationKey dials with the freshly-staged installation key and
// proves possession - the server calls the same Manager.RecordProof
// desktop's proof step already relies on.
func ProveInstallationKey(ctx context.Context, cfg ProofConfig) (operationID, clientID string, err error) {
	result, err := doProofOrFinish(ctx, cfg, proofCommand)
	if err != nil {
		return "", "", err
	}
	if result.Status != "proved" || result.OperationID == "" || result.ClientID == "" {
		return "", "", fmt.Errorf("onboard: unexpected proof result %+v", result)
	}
	return result.OperationID, result.ClientID, nil
}

// FinishActivation dials once more, requesting activation - the server calls
// HasProof+Publish and returns the resulting service revision.
func FinishActivation(ctx context.Context, cfg ProofConfig) (operationID, clientID string, serviceRevision int64, err error) {
	result, err := doProofOrFinish(ctx, cfg, finishCommand)
	if err != nil {
		return "", "", 0, err
	}
	if result.Status != "active" || result.OperationID == "" || result.ClientID == "" || result.ServiceRevision <= 0 {
		return "", "", 0, fmt.Errorf("onboard: unexpected finish result %+v", result)
	}
	return result.OperationID, result.ClientID, result.ServiceRevision, nil
}

func doProofOrFinish(ctx context.Context, cfg ProofConfig, command string) (workerResult, error) {
	hostKey, timeout, err := cfg.validate()
	if err != nil {
		return workerResult{}, err
	}
	config := &ssh.ClientConfig{
		User:              cfg.User,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(cfg.Signer)},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		HostKeyCallback:   pinnedHostKey(hostKey),
		Timeout:           timeout,
	}
	return dialExecAndDecode(ctx, cfg.Address, timeout, config, command, nil)
}

func pinnedHostKey(pinned ssh.PublicKey) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if key.Type() != pinned.Type() || !bytes.Equal(key.Marshal(), pinned.Marshal()) {
			return errors.New("onboard: host key mismatch")
		}
		return nil
	}
}

// dialExecAndDecode is the one-dial-one-session shape every hop in this
// package shares, differing only in auth method, exec command and whether
// there is a stdin payload to send.
func dialExecAndDecode(ctx context.Context, address string, timeout time.Duration, config *ssh.ClientConfig, command string, stdin []byte) (workerResult, error) {
	connection, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return workerResult{}, fmt.Errorf("onboard: dial: %w", err)
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return workerResult{}, fmt.Errorf("onboard: set deadline: %w", err)
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()

	clientConn, channels, requests, err := ssh.NewClientConn(connection, address, config)
	if err != nil {
		return workerResult{}, fmt.Errorf("onboard: handshake: %w", err)
	}
	client := ssh.NewClient(clientConn, channels, requests)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return workerResult{}, fmt.Errorf("onboard: open session: %w", err)
	}
	defer session.Close()

	if stdin != nil {
		session.Stdin = bytes.NewReader(stdin)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return workerResult{}, fmt.Errorf("onboard: stdout pipe: %w", err)
	}
	if err := session.Start(command); err != nil {
		return workerResult{}, fmt.Errorf("onboard: start session: %w", err)
	}

	// Read the full response before Wait, matching pkg/deploy/session.go and
	// pkg/mobileclient/sshtransport.Do: an over-long or hung remote must not
	// be able to block Wait while we are still draining its output.
	var result workerResult
	decodeErr := json.NewDecoder(stdout).Decode(&result)

	if waitErr := session.Wait(); waitErr != nil {
		if decodeErr != nil {
			return workerResult{}, fmt.Errorf("onboard: session failed: %w (result decode: %v)", waitErr, decodeErr)
		}
		return workerResult{}, fmt.Errorf("onboard: session failed: %w", waitErr)
	}
	if decodeErr != nil {
		return workerResult{}, fmt.Errorf("onboard: decode result: %w", decodeErr)
	}
	return result, nil
}
