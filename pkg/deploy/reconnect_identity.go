package deploy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filees/pkg/privatefile"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

type ReconnectIdentity struct {
	DeployRequestID string
	PrivateKeyPath  string
	PublicKey       string
}

// PrepareReconnectIdentity creates or reopens the deployment-scoped key used
// only to re-authorize the outer tunnel after transport loss. The caller owns
// the absolute private root and removes it after activation or expiry.
func PrepareReconnectIdentity(root, deployRequestID string, random io.Reader) (ReconnectIdentity, error) {
	if _, err := uuid.Parse(deployRequestID); err != nil {
		return ReconnectIdentity{}, errors.New("deploy_request_id must be a UUID")
	}
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || !filepath.IsAbs(root) {
		return ReconnectIdentity{}, errors.New("reconnect identity root must be absolute")
	}
	root = filepath.Join(root, deployRequestID)
	if err := privatefile.EnsureDir(root); err != nil {
		return ReconnectIdentity{}, err
	}
	path := filepath.Join(root, "reconnect_ed25519")
	if raw, err := os.ReadFile(path); err == nil {
		defer zero(raw)
		return reconnectIdentityFromPrivate(path, deployRequestID, raw)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ReconnectIdentity{}, err
	}
	if random == nil {
		random = rand.Reader
	}
	_, private, err := ed25519.GenerateKey(random)
	if err != nil {
		return ReconnectIdentity{}, err
	}
	defer zero(private)
	block, err := ssh.MarshalPrivateKey(private, "filees-reconnect:"+deployRequestID)
	if err != nil {
		return ReconnectIdentity{}, err
	}
	raw := pem.EncodeToMemory(block)
	zero(block.Bytes)
	if len(raw) == 0 {
		return ReconnectIdentity{}, errors.New("marshal reconnect private key")
	}
	defer zero(raw)
	created, err := writeBytesExclusive(path, raw, 0o600)
	if err != nil {
		return ReconnectIdentity{}, err
	}
	if !created {
		existing, err := os.ReadFile(path)
		if err != nil {
			return ReconnectIdentity{}, err
		}
		defer zero(existing)
		return reconnectIdentityFromPrivate(path, deployRequestID, existing)
	}
	return reconnectIdentityFromPrivate(path, deployRequestID, raw)
}

func reconnectIdentityFromPrivate(path, deployRequestID string, raw []byte) (ReconnectIdentity, error) {
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return ReconnectIdentity{}, fmt.Errorf("invalid reconnect private key")
	}
	public := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	return ReconnectIdentity{DeployRequestID: deployRequestID, PrivateKeyPath: path, PublicKey: public}, nil
}
