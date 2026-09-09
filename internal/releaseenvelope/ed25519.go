package releaseenvelope

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	signifyAlgorithm  = "Ed"
	signifyKeyIDBytes = 8
	commentPrefix     = "untrusted comment: "
)

// Ed25519Verifier verifies the simple, detached signature format emitted by
// OpenBSD signify. Keys are selected exclusively from the compiled-in keyring.
type Ed25519Verifier struct {
	Keys map[string][]byte
}

// CanonicalSignifyPublicKey validates a signify Ed25519 public key and returns
// its portable two-line representation.  It accepts the two transport defects
// commonly introduced by Windows tooling (CRLF and a missing final newline),
// but rejects every other structural deviation.
func CanonicalSignifyPublicKey(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > 2048 || bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("invalid signify public key")
	}
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	if strings.ContainsRune(normalized, '\r') {
		return nil, errors.New("invalid signify public key line ending")
	}
	normalized = strings.TrimSuffix(normalized, "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], commentPrefix) || len(lines[0]) == len(commentPrefix) {
		return nil, errors.New("invalid signify public key comment")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(lines[1])
	if err != nil || len(decoded) != 2+signifyKeyIDBytes+ed25519.PublicKeySize || string(decoded[:2]) != signifyAlgorithm {
		return nil, errors.New("invalid signify public key payload")
	}
	return []byte(lines[0] + "\n" + lines[1] + "\n"), nil
}

func (verifier Ed25519Verifier) Verify(ctx context.Context, keyID string, message, signature []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	keyID = strings.TrimSpace(keyID)
	encodedKey, ok := verifier.Keys[keyID]
	if !ok || len(encodedKey) == 0 {
		return fmt.Errorf("release key %q is not trusted", keyID)
	}
	if err := VerifySignifySignature(encodedKey, message, signature); err != nil {
		return fmt.Errorf("release key %q: %w", keyID, err)
	}
	return nil
}

// VerifySignifySignature verifies a detached signify signature over message
// against an encoded signify public key.
//
// It is exported for callers that already hold the key as bytes, and the
// server updater is one: its release key is compiled into the binary. Until
// 2026-09-09 that updater wrote the key, the message and the signature into a
// temporary directory for the sole purpose of handing them to signify(1) —
// a process spawn, a filesystem round trip for signing material, and a
// dependency on a program name that Debian gives to an unrelated tool. The
// parsing and the Ed25519 check below were already here the whole time; only
// nothing called them from that side.
func VerifySignifySignature(publicKey, message, signature []byte) error {
	key, err := parseSignifyFile(publicKey, 2+signifyKeyIDBytes+ed25519.PublicKeySize)
	if err != nil {
		return fmt.Errorf("parse release key: %w", err)
	}
	sig, err := parseSignifyFile(signature, 2+signifyKeyIDBytes+ed25519.SignatureSize)
	if err != nil {
		return fmt.Errorf("parse release signature: %w", err)
	}
	if subtle.ConstantTimeCompare(key[2:2+signifyKeyIDBytes], sig[2:2+signifyKeyIDBytes]) != 1 {
		return errors.New("release signature was made with a different key")
	}
	if !ed25519.Verify(ed25519.PublicKey(key[2+signifyKeyIDBytes:]), message, sig[2+signifyKeyIDBytes:]) {
		return errors.New("release signature verification failed")
	}
	return nil
}

func parseSignifyFile(data []byte, decodedSize int) ([]byte, error) {
	if len(data) > 2048 || bytes.IndexByte(data, 0) >= 0 || !bytes.HasSuffix(data, []byte("\n")) {
		return nil, errors.New("invalid signify file")
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], commentPrefix) || len(lines[0]) == len(commentPrefix) || strings.ContainsRune(lines[0], '\r') {
		return nil, errors.New("invalid signify comment")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(lines[1])
	if err != nil || len(decoded) != decodedSize {
		return nil, errors.New("invalid signify base64 payload")
	}
	if string(decoded[:2]) != signifyAlgorithm {
		return nil, errors.New("unsupported signify algorithm")
	}
	return decoded, nil
}
