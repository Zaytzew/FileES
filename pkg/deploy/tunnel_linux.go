//go:build linux

package deploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"filees/pkg/onboarding"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// askpassFIFOEnv is the Linux OTP channel: a FIFO path. Its Windows
// counterpart is a named pipe, so the name stays platform-specific while
// connectKeyEnv/connectRequestIDEnv live in tunnel.go.
const askpassFIFOEnv = "FILEES_ASKPASS_FIFO"

func AskpassConfigured() bool {
	return os.Getenv(askpassFIFOEnv) != "" || os.Getenv(connectKeyEnv) != ""
}

// RunOpenSSHTunnel starts the fixed OpenSSH reverse-forward command. OTP is
// delivered once through an owner-only FIFO in XDG_RUNTIME_DIR; it is never an
// argument or environment value.
func RunOpenSSHTunnel(ctx context.Context, spec TunnelSpec, otp []byte) error {
	if len(otp) == 0 || len(otp) > 1024 || bytes.Contains(otp, []byte{'\r'}) || bytes.Contains(otp, []byte{'\n'}) {
		return errors.New("bootstrap OTP must contain 1..1024 bytes without newlines")
	}
	args, err := OpenSSHArgs(spec)
	if err != nil {
		return err
	}
	frame, err := EncodeTunnelSession(TunnelSession{Schema: TunnelSessionSchema, DeployRequestID: spec.DeployRequestID, HelperHostPublicKey: spec.HelperEndpoint.HostPublicKey, ReconnectPublicKey: spec.ReconnectPublicKey})
	if err != nil {
		return err
	}
	runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	if !filepath.IsAbs(runtimeDir) {
		return errors.New("XDG_RUNTIME_DIR is required for bootstrap OTP handoff")
	}
	dir, err := os.MkdirTemp(runtimeDir, "filees-bootstrap-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	fifo := filepath.Join(dir, "otp.fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	secret := append([]byte(nil), otp...)
	defer zero(secret)
	writerCtx, cancelWriter := context.WithCancel(ctx)
	defer cancelWriter()
	writerDone := make(chan error, 1)
	go func() { writerDone <- writeOTPOnce(writerCtx, fifo, secret) }()

	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(frame)
	diagnostic := &boundedDiagnostic{limit: 16 * 1024}
	cmd.Stderr = diagnostic
	cmd.Stdout = nil
	cmd.Env = scrubEnvironment(os.Environ(), "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "DISPLAY", askpassFIFOEnv, connectKeyEnv, connectRequestIDEnv)
	cmd.Env = append(cmd.Env,
		"SSH_ASKPASS="+executable,
		"SSH_ASKPASS_REQUIRE=force",
		"DISPLAY=filees-bootstrap",
		askpassFIFOEnv+"="+fifo,
	)
	runErr := cmd.Run()
	cancelWriter()
	select {
	case writerErr := <-writerDone:
		if writerErr != nil && runErr == nil {
			runErr = writerErr
		}
	case <-time.After(time.Second):
	}
	if runErr != nil {
		return tunnelCommandError("bootstrap SSH tunnel", runErr, diagnostic.String())
	}
	return nil
}

// RunOpenSSHReconnectTunnel recreates the same fixed reverse forward after
// transport loss. BSD Auth receives a signature over its fresh nonce; the
// one-time mail OTP is neither retained nor reused.
func RunOpenSSHReconnectTunnel(ctx context.Context, spec TunnelSpec, privateKeyPath string) error {
	args, err := OpenSSHArgs(spec)
	if err != nil {
		return err
	}
	signer, err := loadReconnectSigner(privateKeyPath)
	if err != nil {
		return err
	}
	configured, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(spec.ReconnectPublicKey)))
	if err != nil || !bytes.Equal(configured.Marshal(), signer.PublicKey().Marshal()) {
		return errors.New("reconnect private key does not match tunnel binding")
	}
	frame, err := EncodeTunnelSession(TunnelSession{Schema: TunnelSessionSchema, DeployRequestID: spec.DeployRequestID, HelperHostPublicKey: spec.HelperEndpoint.HostPublicKey, ReconnectPublicKey: spec.ReconnectPublicKey})
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(frame)
	diagnostic := &boundedDiagnostic{limit: 16 * 1024}
	cmd.Stderr = diagnostic
	cmd.Env = scrubEnvironment(os.Environ(), "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "DISPLAY", askpassFIFOEnv, connectKeyEnv, connectRequestIDEnv)
	cmd.Env = append(cmd.Env,
		"SSH_ASKPASS="+executable,
		"SSH_ASKPASS_REQUIRE=force",
		"DISPLAY=filees-reconnect",
		connectKeyEnv+"="+filepath.Clean(privateKeyPath),
		connectRequestIDEnv+"="+spec.DeployRequestID,
	)
	if err := cmd.Run(); err != nil {
		return tunnelCommandError("reconnect SSH tunnel", err, diagnostic.String())
	}
	return nil
}

// RunAskpass serves the internal OpenSSH askpass invocation. It accepts only
// the FIFO created by RunOpenSSHTunnel in the current user's runtime directory.
func RunAskpass() error {
	if os.Getenv(connectKeyEnv) != "" {
		if len(os.Args) < 2 {
			return errors.New("reconnect askpass challenge is missing")
		}
		signer, err := loadReconnectSigner(os.Getenv(connectKeyEnv))
		if err != nil {
			return err
		}
		response, err := onboarding.EncodeReconnectResponse(os.Args[len(os.Args)-1], os.Getenv(connectRequestIDEnv), signer)
		if err != nil {
			return err
		}
		_, err = io.WriteString(os.Stdout, response+"\n")
		return err
	}
	fifo := strings.TrimSpace(os.Getenv(askpassFIFOEnv))
	runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	if !filepath.IsAbs(fifo) || !filepath.IsAbs(runtimeDir) {
		return errors.New("askpass FIFO is not configured")
	}
	rel, err := filepath.Rel(runtimeDir, fifo)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !strings.HasPrefix(rel, "filees-bootstrap-") || filepath.Base(fifo) != "otp.fifo" {
		return errors.New("askpass FIFO is outside FileES runtime scope")
	}
	info, err := os.Lstat(fifo)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeNamedPipe == 0 || info.Mode().Perm() != 0o600 {
		return errors.New("askpass path is not an owner-only FIFO")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return errors.New("askpass FIFO is not owned by the current user")
	}
	fd, err := unix.Open(fifo, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), fifo)
	defer file.Close()
	secret := make([]byte, 1025)
	n, err := file.Read(secret)
	if err != nil {
		zero(secret)
		return err
	}
	if n == 0 || n > 1024 {
		zero(secret)
		return errors.New("askpass OTP length is invalid")
	}
	_, writeErr := os.Stdout.Write(append(secret[:n], '\n'))
	zero(secret)
	return writeErr
}

func loadReconnectSigner(path string) (ssh.Signer, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || !filepath.IsAbs(path) {
		return nil, errors.New("reconnect private key path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || stat.Uid != uint32(os.Getuid()) {
		return nil, errors.New("reconnect private key must be an owner-only regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	defer zero(raw)
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, errors.New("reconnect private key must be unencrypted Ed25519")
	}
	return signer, nil
}

func writeOTPOnce(ctx context.Context, fifo string, otp []byte) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		fd, err := unix.Open(fifo, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err == nil {
			file := os.NewFile(uintptr(fd), fifo)
			_, writeErr := file.Write(otp)
			closeErr := file.Close()
			if writeErr != nil {
				return writeErr
			}
			return closeErr
		}
		if !errors.Is(err, unix.ENXIO) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

