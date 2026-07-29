package androidbind

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"filees/internal/durable"

	"golang.org/x/crypto/ssh"
)

// identity is the device's own persistent Ed25519 keypair. It is generated
// once and never leaves the device: the private key is written under the
// caller's non-evictable store root and is never sent to, or accepted from,
// the server (concept doc §2: "Zaufany serwer zleca generację klucza po
// stronie klienta... private key nie opuszcza klienta").
type identity struct {
	signer ssh.Signer
}

// loadOrCreateIdentity loads the identity persisted under storeDir/identity,
// or generates and persists a new one if none exists yet. It is idempotent:
// calling it again against the same storeDir returns the same keypair.
func loadOrCreateIdentity(storeDir string) (identity, error) {
	dir := filepath.Join(storeDir, "identity")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return identity{}, err
	}
	keyPath := filepath.Join(dir, "id_ed25519")

	if raw, err := os.ReadFile(keyPath); err == nil {
		signer, err := ssh.ParsePrivateKey(raw)
		zero(raw)
		if err != nil {
			return identity{}, fmt.Errorf("parse stored identity: %w", err)
		}
		return identity{signer: signer}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return identity{}, err
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return identity{}, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return identity{}, err
	}
	raw := pem.EncodeToMemory(block)
	if err := writeFileAtomic(keyPath, raw, 0o600); err != nil {
		return identity{}, err
	}
	signer, err := ssh.ParsePrivateKey(raw)
	zero(raw)
	if err != nil {
		return identity{}, err
	}
	return identity{signer: signer}, nil
}

// writeFileAtomic writes data to path via a temp file in the same directory,
// fsync, rename, then fsyncs the directory — the same recipe used elsewhere
// in this codebase for durable, crash-safe writes (pkg/mobileclient.Store).
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return durable.SyncDirectory(dir)
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
