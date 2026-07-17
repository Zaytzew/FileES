package deploy

import (
	"os"
	"testing"

	"github.com/google/uuid"
)

func TestReconnectIdentityIsPrivateDurableAndDeploymentScoped(t *testing.T) {
	requestID := uuid.NewString()
	first, err := PrepareReconnectIdentity(t.TempDir(), requestID, nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(first.PrivateKeyPath)
	if err != nil || info.Mode().Perm() != 0o600 || first.PublicKey == "" {
		t.Fatalf("identity=%+v info=%v err=%v", first, info, err)
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
