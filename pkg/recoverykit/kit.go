// Package recoverykit persists the user-owned FileES recovery capability.
package recoverykit

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"time"

	"filees/pkg/repoworker"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const Schema = "filees.recovery-kit/v1"

type Kit struct {
	Schema        string                      `json:"schema"`
	OperationID   string                      `json:"operation_id"`
	RealmID       string                      `json:"realm_id"`
	ServerAddress string                      `json:"server_address"`
	KnownHost     string                      `json:"known_host"`
	PrivateKey    string                      `json:"private_key"`
	Manifest      repoworker.RecoveryManifest `json:"manifest"`
}

func Create(address, knownHost string, manifest repoworker.RecoveryManifest) (Kit, string, error) {
	if manifest.Schema != repoworker.RecoveryManifestSchema || manifest.OperationID == "" || manifest.RealmID == "" || address == "" || knownHost == "" {
		return Kit{}, "", errors.New("recovery kit input is incomplete")
	}
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Kit{}, "", err
	}
	block, err := ssh.MarshalPrivateKey(private, "filees-recovery:"+manifest.OperationID)
	if err != nil {
		return Kit{}, "", err
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		return Kit{}, "", err
	}
	kit := Kit{Schema: Schema, OperationID: manifest.OperationID, RealmID: manifest.RealmID, ServerAddress: address, KnownHost: knownHost, PrivateKey: string(pem.EncodeToMemory(block)), Manifest: manifest}
	return kit, string(ssh.MarshalAuthorizedKey(key)), nil
}

func (k Kit) Validate(now time.Time) error {
	if k.Schema != Schema || k.OperationID == "" || k.RealmID == "" || k.ServerAddress == "" || k.KnownHost == "" || k.PrivateKey == "" {
		return errors.New("recovery kit is incomplete")
	}
	if _, err := uuid.Parse(k.OperationID); err != nil {
		return err
	}
	if _, err := uuid.Parse(k.RealmID); err != nil {
		return err
	}
	if k.Manifest.Schema != repoworker.RecoveryManifestSchema || k.Manifest.OperationID != k.OperationID || k.Manifest.RealmID != k.RealmID || !now.Before(k.Manifest.AdminGraceUntil) {
		return errors.New("recovery kit manifest is invalid or expired")
	}
	private, err := ssh.ParsePrivateKey([]byte(k.PrivateKey))
	if err != nil {
		return errors.New("recovery kit private key is invalid")
	}
	if private.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return errors.New("recovery kit private key must be Ed25519")
	}
	return nil
}

func Store(path string, kit Kit) error {
	if !filepath.IsAbs(path) {
		return errors.New("recovery kit path must be absolute")
	}
	raw, err := json.MarshalIndent(kit, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fkr-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(append(raw, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
