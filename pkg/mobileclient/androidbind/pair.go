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

// PairJSON drives the full mobile pairing sequence end to end
// (concepts/FILEES_ANDROID_CLIENT_CONCEPT_V2.md §4.2): pushes this device's
// already-persisted installation public key (the same identity NewClient
// itself uses - generated once by loadOrCreateIdentity, private key never
// leaves the device) over the pairing token scanned from the desktop's QR,
// proves possession, and requests activation. storeDir must be the same
// non-evictable filesDir NewClient will later use for this same device.
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

	pairing := onboard.PairingConfig{Address: address, HostPublicKey: hostPublicKey}
	_, pushedClientID, err := onboard.PushInstallationKey(ctx, pairing, token, publicKey, fingerprint)
	if err != nil {
		return "", fmt.Errorf("androidbind: push installation key: %w", err)
	}

	proof := onboard.ProofConfig{Address: address, User: mobileOperationalUser, HostPublicKey: hostPublicKey, Signer: ident.signer}
	if _, provedClientID, err := onboard.ProveInstallationKey(ctx, proof); err != nil {
		return "", fmt.Errorf("androidbind: prove installation key: %w", err)
	} else if provedClientID != pushedClientID {
		return "", errors.New("androidbind: client_id mismatch between push and proof")
	}

	_, finishedClientID, revision, err := onboard.FinishActivation(ctx, proof)
	if err != nil {
		return "", fmt.Errorf("androidbind: finish activation: %w", err)
	}
	if finishedClientID != pushedClientID {
		return "", errors.New("androidbind: client_id mismatch between push and finish")
	}
	raw, err := json.Marshal(PairResult{ClientID: finishedClientID, ServiceRevision: revision})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
