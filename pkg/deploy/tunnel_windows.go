//go:build windows

package deploy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"filees/pkg/privatefile"

	"golang.org/x/sys/windows"
)

// askpassPipeEnv carries the name of the named pipe the OTP arrives on. The
// Linux counterpart names a FIFO path (askpassFIFOEnv); the two are separate
// because the objects are, not because the contract differs.
const askpassPipeEnv = "FILEES_ASKPASS_PIPE"

// otpPipePrefix is fixed so the askpass child can reject any name that did
// not come from here. Windows keeps named pipes in one machine-wide
// namespace, so unlike Linux there is no per-user directory to confine them
// to — see concepts/WINDOWS_BOOTSTRAP_CONCEPT.md §4.
const otpPipePrefix = `\\.\pipe\filees-bootstrap-`

func AskpassConfigured() bool {
	return os.Getenv(askpassPipeEnv) != "" || os.Getenv(connectKeyEnv) != ""
}

// RunOpenSSHTunnel starts the fixed OpenSSH reverse-forward command. The OTP
// is delivered once over a named pipe restricted to the current user; it is
// never an argument or an environment value.
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
	executable, err := os.Executable()
	if err != nil {
		return err
	}

	name, pipe, err := createOTPPipe()
	if err != nil {
		return err
	}
	// Closing the handle is also what releases a serveOTPOnce still blocked in
	// ConnectNamedPipe, so it must happen before waiting on writerDone.
	pipeClosed := false
	closePipe := func() {
		if !pipeClosed {
			pipeClosed = true
			_ = windows.CloseHandle(pipe)
		}
	}
	defer closePipe()

	secret := append([]byte(nil), otp...)
	defer zero(secret)
	writerDone := make(chan error, 1)
	go func() { writerDone <- serveOTPOnce(pipe, secret) }()

	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(frame)
	diagnostic := &boundedDiagnostic{limit: 16 * 1024}
	cmd.Stderr = diagnostic
	cmd.Stdout = nil
	cmd.Env = scrubEnvironment(os.Environ(), "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "DISPLAY", askpassPipeEnv, connectKeyEnv, connectRequestIDEnv)
	// No DISPLAY: SSH_ASKPASS_REQUIRE=force is enough on Windows OpenSSH,
	// verified against 9.5p2 before this port was planned.
	cmd.Env = append(cmd.Env,
		"SSH_ASKPASS="+executable,
		"SSH_ASKPASS_REQUIRE=force",
		askpassPipeEnv+"="+name,
	)
	runErr := cmd.Run()
	closePipe()
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

// RunOpenSSHReconnectTunnel is phase 3 of
// concepts/WINDOWS_BOOTSTRAP_CONCEPT.md. It is a distinct mechanism — the
// server challenges a durable key instead of a mail OTP — so it is left
// explicitly unimplemented rather than approximated by the bootstrap path.
func RunOpenSSHReconnectTunnel(context.Context, TunnelSpec, string) error {
	return errors.New("push reconnect tunnel is not implemented on Windows yet")
}

// createOTPPipe publishes a single-instance pipe under an unguessable name,
// readable only by the current user.
//
// FILE_FLAG_FIRST_PIPE_INSTANCE is the squatting guard: creation fails if the
// name already exists, so this process can never end up serving a pipe some
// other process opened first. On Linux the equivalent protection came from
// the FIFO living inside a 0700 directory in XDG_RUNTIME_DIR; the Windows
// pipe namespace offers no such enclosure.
func createOTPPipe() (string, windows.Handle, error) {
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return "", windows.InvalidHandle, err
	}
	name := otpPipePrefix + hex.EncodeToString(suffix)
	pipe, err := createNamedOTPPipe(name)
	if err != nil {
		return "", windows.InvalidHandle, err
	}
	return name, pipe, nil
}

// createNamedOTPPipe is split out so a test can claim a name first and prove
// this call then refuses it. Generating the name here would make that
// impossible to express.
func createNamedOTPPipe(name string) (windows.Handle, error) {
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes, err := privatefile.OwnerOnlyAttributes()
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateNamedPipe(wide,
		windows.PIPE_ACCESS_OUTBOUND|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1, 1024, 1024, 0, attributes)
}

// serveOTPOnce hands the secret to the first client and stops. It mirrors the
// Linux writer, which retries opening the FIFO until a reader appears; here
// the wait is ConnectNamedPipe, and cancellation arrives as a closed handle.
func serveOTPOnce(pipe windows.Handle, otp []byte) error {
	if err := windows.ConnectNamedPipe(pipe, nil); err != nil && err != windows.ERROR_PIPE_CONNECTED {
		return err
	}
	var written uint32
	if err := windows.WriteFile(pipe, otp, &written, nil); err != nil {
		return err
	}
	if int(written) != len(otp) {
		return errors.New("bootstrap OTP was truncated on the way to askpass")
	}
	return windows.FlushFileBuffers(pipe)
}

// RunAskpass serves the internal OpenSSH askpass invocation. It accepts only
// a pipe published by RunOpenSSHTunnel in this session.
func RunAskpass() error {
	if os.Getenv(connectKeyEnv) != "" {
		return errors.New("reconnect askpass is not implemented on Windows yet")
	}
	name := strings.TrimSpace(os.Getenv(askpassPipeEnv))
	if !strings.HasPrefix(name, otpPipePrefix) || len(name) != len(otpPipePrefix)+32 {
		return errors.New("askpass pipe is not configured")
	}
	if _, err := hex.DecodeString(name[len(otpPipePrefix):]); err != nil {
		return errors.New("askpass pipe name is not a FileES bootstrap name")
	}
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(wide, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)

	secret := make([]byte, 1025)
	defer zero(secret)
	var read uint32
	if err := windows.ReadFile(handle, secret, &read, nil); err != nil {
		return err
	}
	if read == 0 || read > 1024 {
		return errors.New("askpass OTP length is invalid")
	}
	_, err = os.Stdout.Write(append(secret[:read:read], '\n'))
	return err
}
