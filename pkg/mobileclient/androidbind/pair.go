package androidbind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"filees/pkg/mobileclient/onboard"

	"golang.org/x/crypto/ssh"
)

const pairTimeout = 30 * time.Second

// mobileOperationalUser is the technical account bound to the operational
// filees-mobile-v1 SSH class (matches the same constant hardcoded in
// MainActivity.kt today) - forced-command dispatch is driven entirely by
// the connecting key's own authorized_keys command= line, not by which
// account name is used to log in, so this value only needs to match
// whatever account Phase 3's sshd wiring ends up using.
const mobileOperationalUser = "_filees-mobile"

// PairResult is PairJSON's result shape.
type PairResult struct {
	ClientID        string `json:"client_id"`
	ServiceRevision int64  `json:"service_revision"`
}

// PairJSON drives the mobile pairing sequence
// (concepts/FILEES_ANDROID_CLIENT_CONCEPT_V2.md §4.2) using this device's
// already-persisted installation public key (the same identity NewClient
// itself uses - generated once by loadOrCreateIdentity, private key never
// leaves the device). It probes with a proof attempt first and only pushes
// the key (spending the pairing token scanned from the desktop's QR) if the
// server does not already recognize it - this makes a retried call safe
// even when an earlier attempt's own response (push or finish) never
// reached the caller, without needing any local checkpoint. storeDir must
// be the same non-evictable filesDir NewClient will later use for this same
// device.
//
// Returns a PairResult as JSON (gomobile cannot bind more than one non-error
// return value, hence JSON here - same convention as RefreshJSON/
// DrainPendingJSON elsewhere in this file). The caller does not need
// anything from the result to then call
// NewClient(storeDir, address, "_filees-mobile", hostPublicKey): that call
// reuses the exact same persisted identity this function just staged and
// activated. The result is informational only (e.g. to show a revision
// number in the UI).
func PairJSON(storeDir, address, hostPublicKey, token string) (string, error) {
	if strings.TrimSpace(storeDir) == "" {
		return "", errors.New("androidbind: store_dir is required")
	}
	ident, err := loadOrCreateIdentity(storeDir)
	if err != nil {
		return "", fmt.Errorf("androidbind: identity: %w", err)
	}
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(ident.signer.PublicKey())))
	fingerprint := ssh.FingerprintSHA256(ident.signer.PublicKey())

	ctx, cancel := context.WithTimeout(context.Background(), pairTimeout)
	defer cancel()

	proof := onboard.ProofConfig{Address: address, User: mobileOperationalUser, HostPublicKey: hostPublicKey, Signer: ident.signer}

	// Probe first with the already-persisted identity: if the server
	// already knows this device's key - staged by an earlier attempt whose
	// own response never reached this call, e.g. the push committed
	// server-side but the client crashed or lost the connection before
	// decoding the result - resume straight from proof instead of
	// re-spending the (possibly single-use) pairing token on a redundant
	// push. Only a confirmed "key unknown" auth rejection falls through to
	// the full push flow below; any other error (network, timeout, host key
	// mismatch) is returned unchanged, with no pairing state touched.
	_, clientID, err := onboard.ProveInstallationKey(ctx, proof)
	switch {
	case err == nil:
		// Already staged from an earlier attempt - proceed straight to finish.
	case onboard.IsKeyUnauthorized(err):
		pairing := onboard.PairingConfig{Address: address, HostPublicKey: hostPublicKey}
		_, pushedClientID, pushErr := onboard.PushInstallationKey(ctx, pairing, token, publicKey, fingerprint)
		if pushErr != nil {
			return "", fmt.Errorf("androidbind: push installation key: %w", pushErr)
		}
		if _, clientID, err = onboard.ProveInstallationKey(ctx, proof); err != nil {
			return "", fmt.Errorf("androidbind: prove installation key: %w", err)
		}
		if clientID != pushedClientID {
			return "", errors.New("androidbind: client_id mismatch between push and proof")
		}
	default:
		return "", fmt.Errorf("androidbind: prove installation key: %w", err)
	}

	_, finishedClientID, revision, err := onboard.FinishActivation(ctx, proof)
	if err != nil {
		return "", fmt.Errorf("androidbind: finish activation: %w", err)
	}
	if finishedClientID != clientID {
		return "", errors.New("androidbind: client_id mismatch between proof and finish")
	}
	raw, err := json.Marshal(PairResult{ClientID: finishedClientID, ServiceRevision: revision})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
