package deploy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filees/pkg/privatefile"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const ActivationSessionSchema = "filees.activation-session/v1"

type ActivationSession struct {
	Schema          string `json:"schema"`
	DeployRequestID string `json:"deploy_request_id"`
}

type ActivationOptions struct {
	Root          string
	ServerProfile ServerProfile
	RemotePort    int
}

type tunnelStarter func(context.Context, TunnelSpec, []byte) error

// RunActivation starts or resumes the client half of deployment. Every value
// later bound by the server is durable before the one-time OTP is handed to
// OpenSSH. A successful return means the remote worker command reached active.
func RunActivation(ctx context.Context, passport OnboardPassport, opts ActivationOptions, otp []byte) error {
	root, err := profileStateRoot(opts.Root, opts.ServerProfile)
	if err != nil {
		return err
	}
	prover, err := NewServiceAccessProver(opts.ServerProfile, filepath.Join(root, "identity"))
	if err != nil {
		return err
	}
	return runActivation(ctx, passport, opts, otp, prover, RunOpenSSHTunnel)
}

// ResumeActivation recreates a tunnel already authorized by a consumed OTP.
// The server challenges the durable reconnect key; no mail secret is retained
// or accepted by this path.
func ResumeActivation(ctx context.Context, passport OnboardPassport, opts ActivationOptions) error {
	root, err := profileStateRoot(opts.Root, opts.ServerProfile)
	if err != nil {
		return err
	}
	prover, err := NewServiceAccessProver(opts.ServerProfile, filepath.Join(root, "identity"))
	if err != nil {
		return err
	}
	session, err := prepareActivationSession(root)
	if err != nil {
		return err
	}
	reconnect, err := PrepareReconnectIdentity(root, session.DeployRequestID, nil)
	if err != nil {
		return err
	}
	return runActivation(ctx, passport, opts, nil, prover, func(ctx context.Context, spec TunnelSpec, _ []byte) error {
		return RunOpenSSHReconnectTunnel(ctx, spec, reconnect.PrivateKeyPath)
	})
}

func runActivation(ctx context.Context, passport OnboardPassport, opts ActivationOptions, otp []byte, prover SSHAccessProver, start tunnelStarter) error {
	if err := passport.validate(); err != nil || passport.State != passportAccepted {
		return errors.New("activation requires an accepted onboard passport")
	}
	root, err := profileStateRoot(opts.Root, opts.ServerProfile)
	if err != nil {
		return err
	}
	identityRoot := filepath.Join(root, "identity")
	if opts.RemotePort < 1 || opts.RemotePort > 65535 {
		return errors.New("activation remote port is invalid")
	}
	workerKey, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(passport.WorkerPublicKey)))
	if err != nil || workerKey.Type() != ssh.KeyAlgoED25519 || comment != "" || len(options) != 0 || len(strings.TrimSpace(string(rest))) != 0 {
		return errors.New("onboard passport worker key is invalid")
	}
	session, err := prepareActivationSession(root)
	if err != nil {
		return err
	}
	reconnect, err := PrepareReconnectIdentity(root, session.DeployRequestID, nil)
	if err != nil {
		return err
	}
	helperSigner, err := prepareHelperHostSigner(root, session.DeployRequestID)
	if err != nil {
		return err
	}
	helper, err := StartHelper(ctx, HelperConfig{
		WorkerKey: workerKey, HostSigner: helperSigner,
		Identity: IdentityGenerator{Root: identityRoot}, Access: prover,
	})
	if err != nil {
		return err
	}
	defer helper.Close()
	spec := TunnelSpec{
		RemotePort: opts.RemotePort, HelperEndpoint: helper.Endpoint(), ServerProfile: opts.ServerProfile,
		DeployRequestID: session.DeployRequestID, ReconnectPublicKey: reconnect.PublicKey,
	}
	if err := start(ctx, spec, otp); err != nil {
		return err
	}
	return nil
}

func prepareActivationSession(root string) (ActivationSession, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return ActivationSession{}, errors.New("activation session root must be absolute")
	}
	if err := privatefile.EnsureDir(root); err != nil {
		return ActivationSession{}, err
	}
	path := filepath.Join(root, "activation.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		wanted := ActivationSession{Schema: ActivationSessionSchema, DeployRequestID: uuid.NewString()}
		created, createErr := writeJSONExclusive(path, wanted, 0o600)
		if createErr != nil {
			return ActivationSession{}, createErr
		}
		if created {
			return wanted, nil
		}
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return ActivationSession{}, err
	}
	var session ActivationSession
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&session); err != nil {
		return ActivationSession{}, fmt.Errorf("decode activation session: %w", err)
	}
	if session.Schema != ActivationSessionSchema {
		return ActivationSession{}, errors.New("unsupported activation session schema")
	}
	if _, err := uuid.Parse(session.DeployRequestID); err != nil {
		return ActivationSession{}, errors.New("activation deploy request ID must be UUID")
	}
	return session, nil
}

func prepareHelperHostSigner(root, deployRequestID string) (ssh.Signer, error) {
	path := filepath.Join(filepath.Clean(root), "helper_host_ed25519")
	if raw, err := os.ReadFile(path); err == nil {
		return parseHelperHostSigner(raw)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	defer zero(private)
	block, err := ssh.MarshalPrivateKey(private, "filees-helper:"+deployRequestID)
	if err != nil {
		return nil, err
	}
	raw := pem.EncodeToMemory(block)
	zero(block.Bytes)
	defer zero(raw)
	created, err := writeBytesExclusive(path, raw, 0o600)
	if err != nil {
		return nil, err
	}
	if !created {
		raw, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
	}
	return parseHelperHostSigner(raw)
}

func parseHelperHostSigner(raw []byte) (ssh.Signer, error) {
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, errors.New("helper host private key is not Ed25519")
	}
	return signer, nil
}
