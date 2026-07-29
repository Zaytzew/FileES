// Package recoverykit persists the user-owned FileES recovery capability.
package recoverykit

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
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
	PublicKey     string                      `json:"public_key"`
	Manifest      repoworker.RecoveryManifest `json:"manifest"`
}

func Create(address, knownHost string, manifest repoworker.RecoveryManifest) (Kit, string, error) {
	kit, publicKey, err := CreateDraft(address, knownHost, manifest.OperationID, manifest.RealmID)
	if err != nil {
		return Kit{}, "", err
	}
	kit, err = Finalize(kit, manifest)
	return kit, publicKey, err
}

func CreateDraft(address, knownHost, operationID, realmID string) (Kit, string, error) {
	if address == "" || knownHost == "" {
		return Kit{}, "", errors.New("recovery kit endpoint is incomplete")
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return Kit{}, "", errors.New("recovery kit operation_id must be UUID")
	}
	if _, err := uuid.Parse(realmID); err != nil {
		return Kit{}, "", errors.New("recovery kit realm_id must be UUID")
	}
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Kit{}, "", err
	}
	block, err := ssh.MarshalPrivateKey(private, "filees-recovery:"+operationID)
	if err != nil {
		return Kit{}, "", err
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		return Kit{}, "", err
	}
	publicKey := string(ssh.MarshalAuthorizedKey(key))
	kit := Kit{Schema: Schema, OperationID: operationID, RealmID: realmID, ServerAddress: address, KnownHost: knownHost, PrivateKey: string(pem.EncodeToMemory(block)), PublicKey: publicKey}
	return kit, publicKey, kit.validateIdentity()
}

func Finalize(kit Kit, manifest repoworker.RecoveryManifest) (Kit, error) {
	if err := kit.validateIdentity(); err != nil {
		return Kit{}, err
	}
	if err := manifest.Validate(); err != nil || manifest.OperationID != kit.OperationID || manifest.RealmID != kit.RealmID {
		return Kit{}, errors.New("recovery manifest does not match kit identity")
	}
	kit.Manifest = manifest
	return kit, nil
}

func (k Kit) Validate(now time.Time) error {
	if err := k.validateIdentity(); err != nil {
		return err
	}
	if err := k.Manifest.Validate(); err != nil || k.Manifest.OperationID != k.OperationID || k.Manifest.RealmID != k.RealmID || !now.Before(k.Manifest.AdminGraceUntil) {
		return errors.New("recovery kit manifest is invalid or expired")
	}
	return nil
}

func (k Kit) validateIdentity() error {
	if k.Schema != Schema || k.OperationID == "" || k.RealmID == "" || k.ServerAddress == "" || k.KnownHost == "" || k.PrivateKey == "" || k.PublicKey == "" {
		return errors.New("recovery kit is incomplete")
	}
	if _, err := uuid.Parse(k.OperationID); err != nil {
		return err
	}
	if _, err := uuid.Parse(k.RealmID); err != nil {
		return err
	}
	if err := validateRecoveryEndpoint(k.ServerAddress, k.KnownHost); err != nil {
		return err
	}
	private, err := ssh.ParsePrivateKey([]byte(k.PrivateKey))
	if err != nil {
		return errors.New("recovery kit private key is invalid")
	}
	if private.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return errors.New("recovery kit private key must be Ed25519")
	}
	public, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(k.PublicKey))
	if err != nil || public.Type() != ssh.KeyAlgoED25519 || comment != "" || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 || !bytes.Equal(public.Marshal(), private.PublicKey().Marshal()) {
		return errors.New("recovery kit public key does not match private key")
	}
	return nil
}

func Load(path string, now time.Time) (Kit, error) {
	kit, err := LoadDraft(path)
	if err != nil {
		return Kit{}, err
	}
	if err := kit.Validate(now); err != nil {
		return Kit{}, err
	}
	return kit, nil
}

func LoadDraft(path string) (Kit, error) {
	if !filepath.IsAbs(path) {
		return Kit{}, errors.New("recovery kit path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Kit{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return Kit{}, errors.New("recovery kit must be a private regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Kit{}, err
	}
	if len(raw) > 1<<20 {
		return Kit{}, errors.New("recovery kit exceeds 1 MiB")
	}
	var kit Kit
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&kit); err != nil {
		return Kit{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Kit{}, errors.New("recovery kit contains trailing data")
	}
	if err := kit.validateIdentity(); err != nil {
		return Kit{}, err
	}
	return kit, nil
}

func Store(path string, kit Kit) error {
	if !filepath.IsAbs(path) {
		return errors.New("recovery kit path must be absolute")
	}
	if err := kit.validateIdentity(); err != nil {
		return err
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
