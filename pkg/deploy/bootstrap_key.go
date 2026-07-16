package deploy

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// The bootstrap key is deliberately a compiled protocol capability, not a
// secret or an installation identity. Possession permits only submission of
// an onboarding request to the forced-command account. The ticket and mailbox
// control remain the actual authorization boundaries.
const bootstrapSeedHex = "7b63ed98c0289c927fd27d391a9f85dd0f07ab142ca003fb241b12d77dbe2884"

const BootstrapKeyFingerprint = "SHA256:bzvYvm/6M/4I4rIhK4xi2F3QSIyDebjeaSxSHo1A3NQ"

func BootstrapSigner() (ssh.Signer, error) {
	seed, err := hex.DecodeString(bootstrapSeedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("invalid compiled bootstrap key seed")
	}
	private := ed25519.NewKeyFromSeed(seed)
	return ssh.NewSignerFromKey(private)
}

func BootstrapAuthorizedKey() (string, error) {
	signer, err := BootstrapSigner()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
}
