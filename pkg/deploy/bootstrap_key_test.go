package deploy

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestCompiledBootstrapKeyIsStableAndPublicCapabilityOnly(t *testing.T) {
	line, err := BootstrapAuthorizedKey()
	if err != nil {
		t.Fatal(err)
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if key.Type() != ssh.KeyAlgoED25519 || !strings.HasPrefix(line, ssh.KeyAlgoED25519+" ") {
		t.Fatalf("unexpected bootstrap key %q", line)
	}
	if got := ssh.FingerprintSHA256(key); got != BootstrapKeyFingerprint {
		t.Fatalf("bootstrap fingerprint changed: got %s want %s, key %s", got, BootstrapKeyFingerprint, line)
	}
	t.Logf("bootstrap authorized key: %s", line)
}
