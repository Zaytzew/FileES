//go:build linux

package deploy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/onboarding"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

func TestAskpassReadsOTPFromScopedOwnerOnlyFIFO(t *testing.T) {
	runtimeDir := t.TempDir()
	dir := filepath.Join(runtimeDir, "filees-bootstrap-test")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "otp.fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv(askpassFIFOEnv, fifo)
	readFD, writeFD, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout := os.Stdout
	os.Stdout = writeFD
	defer func() { os.Stdout = originalStdout }()
	done := make(chan error, 1)
	go func() { done <- RunAskpass() }()
	if err := writeOTPOnce(t.Context(), fifo, []byte("one-time-secret")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_ = writeFD.Close()
	var output bytes.Buffer
	if _, err := output.ReadFrom(readFD); err != nil {
		t.Fatal(err)
	}
	if output.String() != "one-time-secret\n" {
		t.Fatalf("askpass output=%q", output.String())
	}
}

func TestAskpassRejectsOrdinaryFileAndOutsideRuntime(t *testing.T) {
	runtimeDir := t.TempDir()
	ordinary := filepath.Join(runtimeDir, "filees-bootstrap-test", "otp.fifo")
	if err := os.MkdirAll(filepath.Dir(ordinary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ordinary, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv(askpassFIFOEnv, ordinary)
	if err := RunAskpass(); err == nil {
		t.Fatal("askpass accepted an ordinary file")
	}
	t.Setenv(askpassFIFOEnv, filepath.Join(t.TempDir(), "filees-bootstrap-test", "otp.fifo"))
	if err := RunAskpass(); err == nil {
		t.Fatal("askpass accepted path outside runtime")
	}
}

func TestScrubEnvironmentRemovesInheritedAskpassControls(t *testing.T) {
	got := scrubEnvironment([]string{"PATH=/bin", "SSH_ASKPASS=/evil", askpassFIFOEnv + "=/evil", "DISPLAY=:1"}, "SSH_ASKPASS", askpassFIFOEnv, "DISPLAY")
	if len(got) != 1 || got[0] != "PATH=/bin" {
		t.Fatalf("environment=%#v", got)
	}
}

func TestReconnectAskpassSignsServerNonceWithoutOTP(t *testing.T) {
	requestID := uuid.NewString()
	identity, err := PrepareReconnectIdentity(t.TempDir(), requestID, nil)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := onboarding.NewReconnectChallenge(strings.NewReader(strings.Repeat("n", 32)))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(connectKeyEnv, identity.PrivateKeyPath)
	t.Setenv(connectRequestIDEnv, requestID)
	originalArgs, originalStdout := os.Args, os.Stdout
	os.Args = []string{"filees", challenge}
	readFD, writeFD, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeFD
	defer func() { os.Args, os.Stdout = originalArgs, originalStdout }()
	if err := RunAskpass(); err != nil {
		t.Fatal(err)
	}
	_ = writeFD.Close()
	var output bytes.Buffer
	_, _ = output.ReadFrom(readFD)
	response := strings.TrimSpace(output.String())
	if !strings.HasPrefix(response, "FILEES-R1."+requestID+".") {
		t.Fatalf("response=%q", response)
	}
	private, err := loadReconnectSigner(identity.PrivateKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := onboarding.EncodeReconnectResponse(challenge, requestID, private)
	if err != nil || response != expected {
		t.Fatalf("response does not verify: got=%q expected=%q err=%v", response, expected, err)
	}
}
