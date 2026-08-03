package deploy

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"filees/pkg/privatefile"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func TestGenerateInstallationIdentityIsOpenSSHAtomicAndIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	g := IdentityGenerator{Root: root}
	op, clientID := uuid.NewString(), "client-a"
	first, err := g.GenerateInstallationIdentity(op, clientID)
	if err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(root, "id_ed25519")
	privateBefore, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParsePrivateKey(privateBefore); err != nil {
		t.Fatalf("private key is not OpenSSH: %v", err)
	}
	// Ask whether the key is private, not which mechanism made it so. The old
	// "mode != 0o600" spelling could not hold on Windows however tight the
	// DACL was, so it reported a portability problem instead of the real
	// exposure: the private key was inheriting access for a second local
	// account.
	if err := privatefile.Verify(privatePath); err != nil {
		t.Fatalf("installation private key is not private: %v", err)
	}
	if err := privatefile.Verify(filepath.Join(root, "identity.json")); err != nil {
		t.Fatalf("identity state is not private: %v", err)
	}
	second, err := g.GenerateInstallationIdentity(op, clientID)
	if err != nil {
		t.Fatal(err)
	}
	privateAfter, _ := os.ReadFile(privatePath)
	if first.Fingerprint == "" || first.PublicKey == "" || first != second || !bytes.Equal(privateBefore, privateAfter) {
		t.Fatalf("identity was not idempotent: first=%#v second=%#v", first, second)
	}
}

func TestGenerateInstallationIdentityRejectsAnotherOperation(t *testing.T) {
	g := IdentityGenerator{Root: filepath.Join(t.TempDir(), "identity")}
	if _, err := g.GenerateInstallationIdentity(uuid.NewString(), "client-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.GenerateInstallationIdentity(uuid.NewString(), "client-a"); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("error=%v", err)
	}
}

func TestGenerateInstallationIdentityRecoversInterruptedFinalization(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	g := IdentityGenerator{Root: root}
	op := uuid.NewString()
	first, err := g.GenerateInstallationIdentity(op, "client-a")
	if err != nil {
		t.Fatal(err)
	}
	state := first
	state.State, state.PublicKey, state.Fingerprint = identityGenerating, "", ""
	if err := writeJSONAtomic(filepath.Join(root, "identity.json"), state, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "id_ed25519.pub")); err != nil {
		t.Fatal(err)
	}
	recovered, err := g.GenerateInstallationIdentity(op, "client-a")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != identityActive || recovered.Fingerprint != first.Fingerprint || recovered.PublicKey != first.PublicKey {
		t.Fatalf("recovered=%#v first=%#v", recovered, first)
	}
}

func TestConcurrentGenerationPublishesOneIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	g := IdentityGenerator{Root: root}
	op := uuid.NewString()
	results := make([]Identity, 8)
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = g.GenerateInstallationIdentity(op, "client-a")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("generation %d: %v", i, err)
		}
		if results[i].Fingerprint != results[0].Fingerprint {
			t.Fatalf("multiple identities: %#v", results)
		}
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
