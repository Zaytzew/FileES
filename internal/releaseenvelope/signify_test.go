package releaseenvelope

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type fileVerifier struct {
	key, message, signature []byte
	dir                     string
}

func (verifier *fileVerifier) Verify(_ context.Context, keyPath, messagePath, signaturePath string) error {
	verifier.key, _ = os.ReadFile(keyPath)
	verifier.message, _ = os.ReadFile(messagePath)
	verifier.signature, _ = os.ReadFile(signaturePath)
	verifier.dir = filepath.Dir(keyPath)
	return nil
}

func TestSignifyVerifierUsesTrustedKeyAndCleansPrivateTemporaryFiles(t *testing.T) {
	backend := &fileVerifier{}
	root := t.TempDir()
	verifier := SignifyVerifier{Keys: map[string][]byte{"key-1": []byte("public")}, Verifier: backend, TempRoot: root}
	if err := verifier.Verify(context.Background(), "key-1", []byte("message"), []byte("signature")); err != nil {
		t.Fatal(err)
	}
	if string(backend.key) != "public" || string(backend.message) != "message" || string(backend.signature) != "signature" {
		t.Fatalf("verifier inputs = %q / %q / %q", backend.key, backend.message, backend.signature)
	}
	if _, err := os.Stat(backend.dir); !os.IsNotExist(err) {
		t.Fatalf("verification directory survived: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary root = %v, %v", entries, err)
	}
	if err := verifier.Verify(context.Background(), "unknown", nil, nil); err == nil {
		t.Fatal("accepted unknown release key")
	}
}
