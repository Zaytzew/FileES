//go:build windows && store_alias_probe

package deploy

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"golang.org/x/sys/windows"
)

// Run on a machine with the Store package installed, from outside the
// package - where OpenSSH runs:
//
//	go test -c -tags store_alias_probe -o probe.exe ./pkg/deploy
//	set FILEES_PROBE_ALIAS=%LOCALAPPDATA%\Microsoft\WindowsApps\<family>\filees-askpass.exe
//	probe.exe -test.run TestStoreAskpassAliasDeliversTheOTP -test.v
//
// It serves an OTP exactly as RunOpenSSHTunnel does and starts the alias the
// way OpenSSH starts SSH_ASKPASS: environment, inherited stdout, no console.
func TestStoreAskpassAliasDeliversTheOTP(t *testing.T) {
	alias := os.Getenv("FILEES_PROBE_ALIAS")
	if alias == "" {
		t.Skip("FILEES_PROBE_ALIAS is not set")
	}
	name, pipe, err := createOTPPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(pipe)
	served := make(chan error, 1)
	go func() { served <- serveOTPOnce(pipe, []byte("OTP-PROBE")) }()

	cmd := exec.Command(alias)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Env = append(scrubEnvironment(os.Environ(), askpassPipeEnv, askpassServerPIDEnv, connectKeyEnv, connectRequestIDEnv),
		askpassPipeEnv+"="+name,
		askpassServerPIDEnv+"="+strconv.Itoa(os.Getpid()),
	)
	if err := cmd.Run(); err != nil {
		t.Fatalf("alias %s: %v; stderr=%q", alias, err, stderr.String())
	}
	if err := <-served; err != nil {
		t.Fatalf("serving the OTP: %v", err)
	}
	if got := stdout.String(); got != "OTP-PROBE\n" {
		t.Fatalf("alias stdout = %q, want the OTP and one newline; stderr=%q", got, stderr.String())
	}
}
