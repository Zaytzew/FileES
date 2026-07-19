package deploy

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	ServiceClientUser   = "_filees-client"
	ServiceSVNCommand   = "svnserve -t"
	ServiceProofCommand = "filees-proof/v1"
)

type SSHAccessProver struct {
	Address        string
	HostPublicKey  string
	KnownHostsPath string
	IdentityRoot   string
	Timeout        time.Duration
}

func NewServiceAccessProver(profile ServerProfile, identityRoot string) (SSHAccessProver, error) {
	if err := profile.validate(); err != nil {
		return SSHAccessProver{}, err
	}
	knownHostsPath := profile.KnownHostsPath
	if !filepath.IsAbs(identityRoot) || !filepath.IsAbs(knownHostsPath) {
		return SSHAccessProver{}, errors.New("service proof identity and known_hosts paths must be absolute")
	}
	if _, err := knownhosts.New(knownHostsPath); err != nil {
		return SSHAccessProver{}, fmt.Errorf("load service host pin: %w", err)
	}
	host, port := profile.hostAndPort()
	return SSHAccessProver{
		Address: net.JoinHostPort(host, port), KnownHostsPath: filepath.Clean(knownHostsPath),
		IdentityRoot: filepath.Clean(identityRoot), Timeout: 10 * time.Second,
	}, nil
}

func (p SSHAccessProver) ProveServiceAccess(operationID, clientID string) error {
	identity, err := loadIdentity(filepath.Join(p.IdentityRoot, "identity.json"))
	if err != nil {
		return err
	}
	if identity.OperationID != operationID || identity.ClientID != clientID || identity.State != identityActive {
		return errors.New("installation identity does not match service proof")
	}
	raw, err := os.ReadFile(filepath.Join(p.IdentityRoot, "id_ed25519"))
	if err != nil {
		return err
	}
	defer zero(raw)
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return errors.New("installation private key is not Ed25519")
	}
	var hostCallback ssh.HostKeyCallback
	if p.KnownHostsPath != "" {
		hostCallback, err = knownhosts.New(p.KnownHostsPath)
		if err != nil {
			return fmt.Errorf("load service host pin: %w", err)
		}
	} else {
		hostKey, comment, options, rest, parseErr := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(p.HostPublicKey)))
		if parseErr != nil || hostKey.Type() != ssh.KeyAlgoED25519 || comment != "" || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
			return errors.New("service host key pin is invalid")
		}
		hostCallback = func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if !keysEqual(key, hostKey) {
				return errors.New("service host key mismatch")
			}
			return nil
		}
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	connection, err := net.DialTimeout("tcp", p.Address, timeout)
	if err != nil {
		return fmt.Errorf("service proof connect: %w", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(timeout))
	config := &ssh.ClientConfig{
		User: ServiceClientUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, Timeout: timeout,
		HostKeyCallback: hostCallback,
	}
	clientConn, channels, requests, err := ssh.NewClientConn(connection, p.Address, config)
	if err != nil {
		return fmt.Errorf("service proof SSH: %w", err)
	}
	client := ssh.NewClient(clientConn, channels, requests)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin = bytes.NewReader(nil)
	if err := session.Run(ServiceProofCommand); err != nil {
		return fmt.Errorf("service access proof: %w", err)
	}
	return nil
}
