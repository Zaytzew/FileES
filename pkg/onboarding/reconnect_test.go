package onboarding

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func TestReconnectSignatureResumesOnlyLiveBoundDeployment(t *testing.T) {
	now := time.Date(2026, 7, 17, 1, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 42690, 42690)
	defer store.Close()
	_, otp := createTakenOperation(t, store, "reconnect@example.net")
	if _, err := store.AuthenticateOTP(otp); err != nil {
		t.Fatal(err)
	}
	deployRequestID := uuid.NewString()
	signer := newReconnectTestSigner(t)
	public := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if _, err := store.ClaimAuthorizedHelper(42690, deployRequestID, public, public); err != nil {
		t.Fatal(err)
	}
	otherPublic := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(newReconnectTestSigner(t).PublicKey())))
	if _, err := store.ClaimAuthorizedHelper(42690, deployRequestID, public, otherPublic); !errors.Is(err, ErrTunnelGrant) {
		t.Fatalf("different reconnect key resumed deployment: %v", err)
	}
	challenge, err := NewReconnectChallenge(strings.NewReader(strings.Repeat("n", 32)))
	if err != nil {
		t.Fatal(err)
	}
	response, err := EncodeReconnectResponse(challenge, deployRequestID, signer)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := store.AuthenticateReconnect(challenge, response)
	if err != nil || grant.AssignedReversePort != 42690 {
		t.Fatalf("grant=%+v err=%v", grant, err)
	}
	otherChallenge, _ := NewReconnectChallenge(strings.NewReader(strings.Repeat("x", 32)))
	if _, err := store.AuthenticateReconnect(otherChallenge, response); !errors.Is(err, ErrTunnelGrant) {
		t.Fatalf("replayed signature error=%v", err)
	}
	wrong, _ := EncodeReconnectResponse(challenge, deployRequestID, newReconnectTestSigner(t))
	if _, err := store.AuthenticateReconnect(challenge, wrong); !errors.Is(err, ErrTunnelGrant) {
		t.Fatalf("wrong key error=%v", err)
	}

	operation, err := store.PendingActivation(OperationHelperAnnounced)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteGeneratedIdentity(operation.OperationID, deployRequestID, "ssh-ed25519 installation filees:test", "SHA256:test"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteAccessStaged(operation.OperationID, deployRequestID); err != nil {
		t.Fatal(err)
	}
	if err := store.CompletePossessionProof(operation.OperationID, deployRequestID); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteActivation(operation.OperationID, deployRequestID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthenticateReconnect(challenge, response); !errors.Is(err, ErrTunnelGrant) {
		t.Fatalf("active operation accepted reconnect: %v", err)
	}
}

func TestReconnectRejectsExpiredOperation(t *testing.T) {
	now := time.Date(2026, 7, 17, 2, 0, 0, 0, time.UTC)
	store, _ := openTestStore(t, &now, 3, 42691, 42691)
	defer store.Close()
	_, otp := createTakenOperation(t, store, "expired-reconnect@example.net")
	_, _ = store.AuthenticateOTP(otp)
	deployRequestID := uuid.NewString()
	signer := newReconnectTestSigner(t)
	public := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	_, _ = store.ClaimAuthorizedHelper(42691, deployRequestID, public, public)
	challenge, _ := NewReconnectChallenge(strings.NewReader(strings.Repeat("n", 32)))
	response, _ := EncodeReconnectResponse(challenge, deployRequestID, signer)
	now = now.Add(31 * time.Minute)
	if _, err := store.AuthenticateReconnect(challenge, response); !errors.Is(err, ErrTunnelGrant) {
		t.Fatalf("expired reconnect error=%v", err)
	}
}

func TestReconnectChallengeAcceptsOnlyOpenSSHPromptDecoration(t *testing.T) {
	challenge, _ := NewReconnectChallenge(strings.NewReader(strings.Repeat("n", 32)))
	if _, err := ParseReconnectChallenge("(_filees-tunnel@127.0.0.1) " + challenge); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"prefix " + challenge, "(host) injected\n" + challenge, challenge + "trailing"} {
		if _, err := ParseReconnectChallenge(invalid); err == nil {
			t.Fatalf("accepted decorated challenge %q", invalid)
		}
	}
}

func newReconnectTestSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
