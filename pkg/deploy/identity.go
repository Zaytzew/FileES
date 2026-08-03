// Package deploy implements the client side of FileES push deployment.
package deploy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/privatefile"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const IdentitySchema = "filees.installation-identity/v1"

const (
	identityGenerating = "generating"
	identityActive     = "active"
)

var ErrIdentityConflict = errors.New("installation identity belongs to another deploy operation")

type Identity struct {
	Schema         string `json:"schema"`
	OperationID    string `json:"operation_id"`
	ClientID       string `json:"client_id"`
	State          string `json:"state"`
	PublicKey      string `json:"public_key,omitempty"`
	Fingerprint    string `json:"fingerprint,omitempty"`
	PrivateKeyPath string `json:"private_key_path,omitempty"`
}

type IdentityGenerator struct {
	Root   string
	Random io.Reader
}

// GenerateInstallationIdentity creates or resumes one locally-owned Ed25519
// identity. The destination is selected by the client; no server-provided path
// is accepted. Repeating the same operation is idempotent, while another
// operation can never replace an existing private key.
func (g IdentityGenerator) GenerateInstallationIdentity(operationID, clientID string) (Identity, error) {
	if _, err := uuid.Parse(operationID); err != nil {
		return Identity{}, fmt.Errorf("operation_id must be UUID: %w", err)
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return Identity{}, errors.New("client_id is required")
	}
	if !filepath.IsAbs(g.Root) {
		return Identity{}, errors.New("identity root must be absolute")
	}
	root := filepath.Clean(g.Root)
	if err := privatefile.EnsureDir(root); err != nil {
		return Identity{}, fmt.Errorf("secure identity root: %w", err)
	}

	statePath := filepath.Join(root, "identity.json")
	privatePath := filepath.Join(root, "id_ed25519")
	publicPath := privatePath + ".pub"
	wanted := Identity{Schema: IdentitySchema, OperationID: operationID, ClientID: clientID, State: identityGenerating, PrivateKeyPath: privatePath}

	current, err := loadIdentity(statePath)
	if errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Lstat(privatePath); statErr == nil {
			// Another process may have published state and the private key after
			// our first ENOENT. Re-read before classifying the key as orphaned.
			current, err = loadIdentity(statePath)
			if errors.Is(err, os.ErrNotExist) {
				return Identity{}, fmt.Errorf("%w: private key exists without deploy state", ErrIdentityConflict)
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return Identity{}, statErr
		}
		if errors.Is(err, os.ErrNotExist) {
			created, createErr := writeJSONExclusive(statePath, wanted, 0o600)
			if createErr != nil {
				return Identity{}, createErr
			}
			if !created {
				current, err = loadIdentity(statePath)
			} else {
				current, err = wanted, nil
			}
		}
	}
	if err != nil {
		return Identity{}, err
	}
	if current.Schema != IdentitySchema || current.OperationID != operationID || current.ClientID != clientID {
		return Identity{}, ErrIdentityConflict
	}

	if raw, readErr := os.ReadFile(privatePath); readErr == nil {
		return finishIdentity(statePath, publicPath, current, raw)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return Identity{}, readErr
	}

	random := g.Random
	if random == nil {
		random = rand.Reader
	}
	public, private, err := ed25519.GenerateKey(random)
	if err != nil {
		return Identity{}, fmt.Errorf("generate Ed25519 identity: %w", err)
	}
	defer zero(private)
	block, err := ssh.MarshalPrivateKey(private, "filees:"+clientID+":"+operationID)
	if err != nil {
		return Identity{}, fmt.Errorf("marshal OpenSSH private key: %w", err)
	}
	privatePEM := pem.EncodeToMemory(block)
	zero(block.Bytes)
	if len(privatePEM) == 0 {
		return Identity{}, errors.New("marshal OpenSSH private key: empty output")
	}
	defer zero(privatePEM)
	created, err := writeBytesExclusive(privatePath, privatePEM, 0o600)
	if err != nil {
		return Identity{}, err
	}
	if !created {
		existing, readErr := os.ReadFile(privatePath)
		if readErr != nil {
			return Identity{}, readErr
		}
		return finishIdentity(statePath, publicPath, current, existing)
	}
	sshPublic, err := ssh.NewPublicKey(public)
	if err != nil {
		return Identity{}, fmt.Errorf("marshal Ed25519 public key: %w", err)
	}
	return persistFinishedIdentity(statePath, publicPath, current, sshPublic)
}

func finishIdentity(statePath, publicPath string, current Identity, privatePEM []byte) (Identity, error) {
	private, err := ssh.ParseRawPrivateKey(privatePEM)
	if err != nil {
		return Identity{}, fmt.Errorf("parse existing installation private key: %w", err)
	}
	var ed ed25519.PrivateKey
	switch key := private.(type) {
	case ed25519.PrivateKey:
		ed = key
	case *ed25519.PrivateKey:
		ed = *key
	default:
		return Identity{}, errors.New("existing installation private key is not Ed25519")
	}
	defer zero(ed)
	public, err := ssh.NewPublicKey(ed.Public())
	if err != nil {
		return Identity{}, err
	}
	return persistFinishedIdentity(statePath, publicPath, current, public)
}

func persistFinishedIdentity(statePath, publicPath string, current Identity, public ssh.PublicKey) (Identity, error) {
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))) + " filees:" + current.ClientID + "\n"
	if err := writeBytesAtomic(publicPath, []byte(line), 0o644); err != nil {
		return Identity{}, fmt.Errorf("write installation public key: %w", err)
	}
	current.State = identityActive
	current.PublicKey = strings.TrimSpace(line)
	current.Fingerprint = ssh.FingerprintSHA256(public)
	if err := writeJSONAtomic(statePath, current, 0o600); err != nil {
		return Identity{}, fmt.Errorf("activate installation identity: %w", err)
	}
	return current, nil
}

func loadIdentity(path string) (Identity, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Identity{}, err
	}
	var state Identity
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return Identity{}, fmt.Errorf("decode installation identity state: %w", err)
	}
	return state, nil
}

// LoadActiveIdentity returns the durable installation identity after the
// server has completed activation. Callers use it to build local attachments;
// it never reads or returns private key bytes.
func LoadActiveIdentity(identityRoot string) (Identity, error) {
	if !filepath.IsAbs(identityRoot) {
		return Identity{}, errors.New("installation identity root must be absolute")
	}
	identity, err := loadIdentity(filepath.Join(filepath.Clean(identityRoot), "identity.json"))
	if err != nil {
		return Identity{}, err
	}
	if identity.State != identityActive || strings.TrimSpace(identity.ClientID) == "" || identity.PrivateKeyPath != filepath.Join(filepath.Clean(identityRoot), "id_ed25519") {
		return Identity{}, errors.New("installation identity is not active or is inconsistent")
	}
	return identity, nil
}

func writeJSONExclusive(path string, value any, mode os.FileMode) (bool, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return false, err
	}
	return writeBytesExclusive(path, append(raw, '\n'), mode)
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeBytesAtomic(path, append(raw, '\n'), mode)
}

// writeBytesExclusive publishes through link(2): the destination appears
// atomically and can never replace an existing identity, even across processes.
func writeBytesExclusive(path string, raw []byte, mode os.FileMode) (bool, error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".identity-*.tmp")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return false, err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := hardenIfPrivate(tmpPath, mode); err != nil {
		return false, err
	}
	if err := os.Link(tmpPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, syncDir(dir)
}

func writeBytesAtomic(path string, raw []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".identity-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
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
	if err := hardenIfPrivate(tmpPath, mode); err != nil {
		return err
	}
	if err := replaceAtomic(tmpPath, path); err != nil {
		return err
	}
	return syncDir(dir)
}

// replaceAtomic publishes tmpPath over path. POSIX rename(2) always succeeds
// against a concurrent replace of the same target; Windows MoveFileEx does
// not — two publishers racing on one destination can lose with
// ERROR_ACCESS_DENIED even though neither holds the file open. The identity
// generator deliberately lets several goroutines publish the same state, so
// that difference surfaced as a flaky "Access is denied" rather than as the
// last-writer-wins the code intends.
//
// The retry is bounded and the final error is still returned, so a genuine
// permission fault is reported rather than hidden behind a delay. On unix the
// loop never runs a second time.
func replaceAtomic(tmpPath, path string) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		if err = os.Rename(tmpPath, path); err == nil {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return err
}

// hardenIfPrivate restricts the temporary file to its owner before it is
// published, and only when the caller asked for a private mode. Two details
// are load-bearing:
//
// The requested mode is the intent. 0o600 means key material; 0o644 means
// this is the public half and is meant to be readable. Hardening everything
// would make the published public key exclusive.
//
// It must run before the link/rename, not after. Hardening the published path
// held a handle on it, and a concurrent publisher's atomic replace then failed
// with "Access is denied" on Windows — the identity generator relies on that
// replace being atomic. Doing it here also means the file never exists at its
// final path in a permissive state, so there is no window to lose the race in.
func hardenIfPrivate(path string, mode os.FileMode) error {
	if mode.Perm()&0o077 != 0 {
		return nil
	}
	return privatefile.Harden(path)
}

func zero(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
