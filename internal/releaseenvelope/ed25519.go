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

func (verifier Ed25519Verifier) Verify(ctx context.Context, keyID string, message, signature []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	keyID = strings.TrimSpace(keyID)
	encodedKey, ok := verifier.Keys[keyID]
	if !ok || len(encodedKey) == 0 {
		return fmt.Errorf("release key %q is not trusted", keyID)
	}
	key, err := parseSignifyFile(encodedKey, 2+signifyKeyIDBytes+ed25519.PublicKeySize)
	if err != nil {
		return fmt.Errorf("parse trusted release key %q: %w", keyID, err)
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
