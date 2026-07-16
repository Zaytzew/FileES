//go:build linux

package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const askpassFIFOEnv = "FILEES_ASKPASS_FIFO"

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
	frame, err := EncodeTunnelSession(TunnelSession{Schema: TunnelSessionSchema, DeployRequestID: spec.DeployRequestID, HelperHostPublicKey: spec.HelperEndpoint.HostPublicKey})
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
	cmd.Stderr = nil
	cmd.Stdout = nil
	cmd.Env = scrubEnvironment(os.Environ(), "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "DISPLAY", askpassFIFOEnv)
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
		return fmt.Errorf("bootstrap SSH tunnel: %w", runErr)
	}
	return nil
}

// RunAskpass serves the internal OpenSSH askpass invocation. It accepts only
// the FIFO created by RunOpenSSHTunnel in the current user's runtime directory.
func RunAskpass() error {
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

func scrubEnvironment(environment []string, names ...string) []string {
	blocked := make(map[string]bool, len(names))
	for _, name := range names {
		blocked[name] = true
	}
	out := environment[:0:0]
	for _, entry := range environment {
		name := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			name = entry[:index]
		}
		if !blocked[name] {
			out = append(out, entry)
		}
	}
	return out
}
