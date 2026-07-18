package deploy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestOpenBSDCompiledBootstrapIntegration(t *testing.T) {
	address := os.Getenv("FILEES_S2_LAB_ADDRESS")
	knownHosts := os.Getenv("FILEES_S2_LAB_KNOWN_HOSTS")
	email := os.Getenv("FILEES_S2_LAB_EMAIL")
	if address == "" || knownHosts == "" {
		t.Skip("set FILEES_S2_LAB_ADDRESS and FILEES_S2_LAB_KNOWN_HOSTS")
	}
	if email == "" {
		email = "s2-lab@example.net"
	}
	passportRoot := os.Getenv("FILEES_S2_LAB_PASSPORT_ROOT")
	if passportRoot == "" {
		passportRoot = filepath.Join(t.TempDir(), "passport")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	profile := ServerProfile{ID: "openbsd-lab", Address: address, KnownHostsPath: knownHosts}
	passport, err := BeginOnboarding(ctx, passportRoot, profile, email)
	if err != nil {
		t.Fatal(err)
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(passport.WorkerPublicKey))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("onboarding_request_id=%s worker=%s passport=%s", passport.OnboardingRequestID, ssh.FingerprintSHA256(key), filepath.Join(passportRoot, profile.ID, "onboard.json"))
}
