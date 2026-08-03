package deploy

import (
	"testing"

	"filees/pkg/privatefile"

	"github.com/google/uuid"
)

func TestReconnectIdentityIsPrivateDurableAndDeploymentScoped(t *testing.T) {
	requestID := uuid.NewString()
	first, err := PrepareReconnectIdentity(t.TempDir(), requestID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.PublicKey == "" {
		t.Fatalf("identity=%+v", first)
	}
	if err := privatefile.Verify(first.PrivateKeyPath); err != nil {
		t.Fatalf("reconnect private key is not private: %v", err)
	}
	root := t.TempDir()
	stable, err := PrepareReconnectIdentity(root, requestID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := PrepareReconnectIdentity(root, requestID, nil)
	if err != nil || resumed != stable {
		t.Fatalf("resumed=%+v stable=%+v err=%v", resumed, stable, err)
	}
	other, err := PrepareReconnectIdentity(root, uuid.NewString(), nil)
	if err != nil || other.PrivateKeyPath == stable.PrivateKeyPath || other.PublicKey == stable.PublicKey {
		t.Fatalf("other deployment identity=%+v err=%v", other, err)
	}
}
