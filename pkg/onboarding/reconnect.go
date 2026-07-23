package onboarding

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const (
	ReconnectChallengePrefix = "FileES authentication nonce: "
	reconnectResponsePrefix  = "FILEES-R1"
	reconnectDomain          = "filees-reconnect-v1\x00"
)

// NewReconnectChallenge creates an enumeration-free, stateless BSD Auth
// challenge. The response proves possession of the key already bound to the
// deployment; no server-side nonce record is needed.
func NewReconnectChallenge(random io.Reader) (string, error) {
	if random == nil {
		random = rand.Reader
	}
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(random, nonce); err != nil {
		return "", err
	}
	return ReconnectChallengePrefix + base64.RawURLEncoding.EncodeToString(nonce) + ":\n", nil
}

func ParseReconnectChallenge(challenge string) ([]byte, error) {
	value := strings.TrimSuffix(challenge, "\n")
	index := strings.Index(value, ReconnectChallengePrefix)
	if index < 0 || !strings.HasSuffix(value, ":") {
		return nil, errors.New("invalid reconnect challenge")
	}
	leading := value[:index]
	if leading != "" && (len(leading) > 256 || !strings.HasPrefix(leading, "(") || !strings.HasSuffix(leading, ") ") || strings.ContainsAny(leading, "\r\n")) {
		return nil, errors.New("invalid reconnect challenge")
	}
	nonceText := strings.TrimSuffix(value[index+len(ReconnectChallengePrefix):], ":")
	nonce, err := base64.RawURLEncoding.DecodeString(nonceText)
	if err != nil || len(nonce) != 32 {
		return nil, errors.New("invalid reconnect challenge")
	}
	return nonce, nil
}

func EncodeReconnectResponse(challenge, deployRequestID string, signer ssh.Signer) (string, error) {
	nonce, err := ParseReconnectChallenge(challenge)
	if err != nil {
		return "", err
	}
	if _, err := uuid.Parse(deployRequestID); err != nil {
		return "", errors.New("deploy_request_id must be a UUID")
	}
	if signer == nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return "", errors.New("reconnect signer must be Ed25519")
	}
	signature, err := signer.Sign(rand.Reader, reconnectMessage(nonce, deployRequestID))
	if err != nil {
		return "", err
	}
	if signature.Format != ssh.KeyAlgoED25519 {
		return "", errors.New("reconnect signature is not Ed25519")
	}
	return strings.Join([]string{reconnectResponsePrefix, deployRequestID, base64.RawURLEncoding.EncodeToString(signature.Blob)}, "."), nil
}

func decodeReconnectResponse(value string) (string, *ssh.Signature, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != reconnectResponsePrefix {
		return "", nil, errors.New("invalid reconnect response")
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return "", nil, errors.New("invalid reconnect response")
	}
	blob, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(blob) != 64 {
		return "", nil, errors.New("invalid reconnect response")
	}
	return parts[1], &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: blob}, nil
}

func reconnectMessage(nonce []byte, deployRequestID string) []byte {
	message := make([]byte, 0, len(reconnectDomain)+len(nonce)+1+len(deployRequestID))
	message = append(message, reconnectDomain...)
	message = append(message, nonce...)
	message = append(message, 0)
	return append(message, deployRequestID...)
}

// validateBareEd25519PublicKey is shared by the reconnect-key path and
// ClaimAuthorizedMobilePush - both need one bare (no comment/options) Ed25519
// authorized_keys line.
func validateBareEd25519PublicKey(value string) error {
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(value)))
	if err != nil || key.Type() != ssh.KeyAlgoED25519 || comment != "" || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return errors.New("public key must be one bare ssh-ed25519 key")
	}
	return nil
}

// AuthenticateReconnect verifies a signed response against the live operation
// selected by its idempotent deploy request. It never revives an expired or
// completed operation and does not consume or recreate OTP material.
func (s *Files) AuthenticateReconnect(challenge, response string) (AuthGrant, error) {
	if err := s.requireAreas(AreaOperations); err != nil {
		return AuthGrant{}, err
	}
	nonce, err := ParseReconnectChallenge(challenge)
	if err != nil {
		return AuthGrant{}, ErrTunnelGrant
	}
	deployRequestID, signature, err := decodeReconnectResponse(response)
	if err != nil {
		return AuthGrant{}, ErrTunnelGrant
	}
	var grant AuthGrant
	err = s.withLock(func() error {
		path, bundle, err := s.findBundleLocked(func(bundle Bundle) bool {
			return bundle.Operation.DeployRequestID == deployRequestID
		})
		if err != nil {
			return ErrTunnelGrant
		}
		op := &bundle.Operation
		now := s.clock.Now().UTC()
		if !helperDeploymentInProgress(op.State) || op.State == OperationActive || !now.Before(op.ExpiresAt) || validateBareEd25519PublicKey(op.ReconnectPublicKey) != nil {
			return ErrTunnelGrant
		}
		key, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(op.ReconnectPublicKey))
		if err := key.Verify(reconnectMessage(nonce, deployRequestID), signature); err != nil {
			return ErrTunnelGrant
		}
		bundle.addAudit("tunnel_reconnect_authorized", "filees-ssh-auth", now)
		if err := atomicWriteJSON(path, bundle); err != nil {
			return fmt.Errorf("persist reconnect authorization: %w", err)
		}
		grant = authGrantFor(*op)
		return nil
	})
	return grant, err
}
