package deploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filees/internal/processoutput"
	"filees/pkg/privatefile"

	"golang.org/x/crypto/ssh"
)

const (
	OnboardUser         = "_filees-onboard"
	TunnelUser          = "_filees-tunnel"
	TunnelServerCommand = "filees tunnel-v1"
)

// boundedDiagnostic keeps at most limit bytes of the tunnel command's stderr
// so a failing session can explain itself without an unbounded buffer.
type boundedDiagnostic struct {
	data  []byte
	limit int
}

func (w *boundedDiagnostic) Write(p []byte) (int, error) {
	wanted := len(p)
	remaining := w.limit - len(w.data)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		w.data = append(w.data, p...)
	}
	return wanted, nil
}

func (w *boundedDiagnostic) String() string {
	return strings.TrimSpace(processoutput.Text(w.data))
}

// loadReconnectSigner reads the durable key the server challenges after
// transport loss. It is shared rather than per-platform because the reconnect
// path has no OTP channel to differ over — the secret is the key file itself,
// and "only its owner may read it" is exactly what privatefile expresses. The
// Linux version used to spell that as an explicit 0600 plus a Stat_t uid
// comparison, which is the same rule written in a form Windows cannot honour.
func loadReconnectSigner(path string) (ssh.Signer, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || !filepath.IsAbs(path) {
		return nil, errors.New("reconnect private key path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("reconnect private key must be a regular file")
	}
	if err := privatefile.Verify(path); err != nil {
		return nil, fmt.Errorf("reconnect private key must be owner-only: %w", err)
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

func tunnelCommandError(label string, err error, diagnostic string) error {
	if diagnostic == "" {
		return fmt.Errorf("%s: %w", label, err)
	}
	return fmt.Errorf("%s: %w: %s", label, err, diagnostic)
}

type TunnelSpec struct {
	RemotePort         int
	HelperEndpoint     HelperEndpoint
	DeployRequestID    string
	ReconnectPublicKey string
	ServerProfile      ServerProfile
}
