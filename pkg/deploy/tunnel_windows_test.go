//go:build windows

package deploy

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Each test here is tied to a numbered property in
// concepts/WINDOWS_BOOTSTRAP_CONCEPT.md §3, so the exit gate can be checked
// against the table rather than against a count of tests.

// B4: the writer waits for a reader and hands the secret over exactly once.
func TestOTPPipeDeliversTheSecretToOneReader(t *testing.T) {
	name, pipe, err := createOTPPipe()
	if err != nil {
		t.Fatalf("createOTPPipe: %v", err)
	}
	defer windows.CloseHandle(pipe)

	served := make(chan error, 1)
	go func() { served <- serveOTPOnce(pipe, []byte("OTP-CODE")) }()

	got := readPipe(t, name)
	if got != "OTP-CODE" {
		t.Fatalf("read %q, want %q", got, "OTP-CODE")
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serveOTPOnce: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveOTPOnce did not return after the reader took the secret")
	}
}

// B3: the squatting guard. An attacker who creates the name first must make
// our creation fail rather than let us serve a pipe we do not own. This is
// the property with no Linux counterpart, because the Windows pipe namespace
// is machine-wide.
func TestOTPPipeRefusesAnAlreadyClaimedName(t *testing.T) {
	name := otpPipePrefix + strings.Repeat("ab", 16)

	// The squatter goes first, and does not use FIRST_PIPE_INSTANCE — it has
	// no reason to. Without the flag on our side, our creation would simply
	// add a second instance and we would serve the secret on a pipe another
	// process already owns a handle to.
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	squatter, err := windows.CreateNamedPipe(wide,
		windows.PIPE_ACCESS_OUTBOUND, windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT,
		windows.PIPE_UNLIMITED_INSTANCES, 1024, 1024, 0, nil)
	if err != nil {
		t.Fatalf("could not stage a squatted pipe: %v", err)
	}
	defer windows.CloseHandle(squatter)

	pipe, err := createNamedOTPPipe(name)
	if err == nil {
		windows.CloseHandle(pipe)
		t.Fatal("createNamedOTPPipe accepted a name another process had already claimed")
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_PIPE_BUSY) {
		t.Fatalf("unexpected error for a claimed name: %v", err)
	}
}

// B2: the pipe must not be reachable by anyone but its owner. The DACL comes
// from privatefile, so this asserts the wiring rather than re-testing the ACL.
func TestOTPPipeCarriesAnOwnerOnlyDACL(t *testing.T) {
	name, pipe, err := createOTPPipe()
	if err != nil {
		t.Fatalf("createOTPPipe: %v", err)
	}
	defer windows.CloseHandle(pipe)

	sd, err := windows.GetSecurityInfo(pipe, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetSecurityInfo: %v", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("DACL: %v", err)
	}
	if dacl == nil {
		t.Fatalf("%s has a null DACL, which grants everyone access", name)
	}
	if dacl.AceCount != 1 {
		t.Fatalf("DACL has %d entries, want exactly one (the current user)", dacl.AceCount)
	}
}

// B3 again, from the child's side: a name that did not come from
// createOTPPipe must be refused before anything is read from it.
func TestAskpassRejectsForeignPipeNames(t *testing.T) {
	for _, name := range []string{
		"",
		`\\.\pipe\something-else`,
		otpPipePrefix,                           // right prefix, no random part
		otpPipePrefix + "short",                 // wrong length
		otpPipePrefix + strings.Repeat("z", 32), // right length, not hex
		`\\evil-host\pipe\filees-bootstrap-` + strings.Repeat("a", 32), // remote namespace
	} {
		t.Setenv(askpassPipeEnv, name)
		err := RunAskpass()
		if err == nil {
			t.Fatalf("RunAskpass accepted %q", name)
		}
		if !strings.Contains(err.Error(), "askpass pipe") {
			t.Fatalf("RunAskpass(%q) = %v, want a refusal naming the pipe", name, err)
		}
	}
}

// B1 and B8: the secret travels on the pipe, never in the environment handed
// to ssh, and a hostile SSH_ASKPASS inherited from the caller is dropped.
func TestBootstrapEnvironmentCarriesNoSecretAndDropsInheritedAskpass(t *testing.T) {
	inherited := []string{"PATH=C:\\Windows", "SSH_ASKPASS=C:\\evil.exe", "SSH_ASKPASS_REQUIRE=never", "DISPLAY=:1", askpassPipeEnv + `=\\.\pipe\evil`}
	scrubbed := scrubEnvironment(inherited, "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "DISPLAY", askpassPipeEnv, connectKeyEnv, connectRequestIDEnv)
	for _, entry := range scrubbed {
		if strings.HasPrefix(entry, "SSH_ASKPASS") || strings.HasPrefix(entry, "DISPLAY") || strings.HasPrefix(entry, askpassPipeEnv) {
			t.Fatalf("inherited %q survived scrubbing", entry)
		}
	}
	name, pipe, err := createOTPPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(pipe)
	final := append(scrubbed, "SSH_ASKPASS=self.exe", "SSH_ASKPASS_REQUIRE=force", askpassPipeEnv+"="+name)
	for _, entry := range final {
		if strings.Contains(entry, "OTP-CODE") {
			t.Fatalf("environment carries the secret: %q", entry)
		}
	}
}

// B5: a reply outside 1..1024 bytes is refused rather than forwarded to ssh.
func TestAskpassRejectsAnOversizedReply(t *testing.T) {
	name, pipe, err := createOTPPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(pipe)
	go func() { _ = serveOTPOnce(pipe, bytes.Repeat([]byte("x"), 1025)) }()

	t.Setenv(askpassPipeEnv, name)
	stdout, restore := captureStdout(t)
	err = RunAskpass()
	restore()
	if err == nil {
		t.Fatalf("RunAskpass accepted an oversized OTP; stdout=%q", stdout())
	}
	if !strings.Contains(err.Error(), "length is invalid") {
		t.Fatalf("RunAskpass = %v, want a length refusal", err)
	}
}

// The happy path end to end: RunAskpass prints exactly the secret plus the
// newline OpenSSH expects, and nothing else.
func TestAskpassPrintsTheSecretWithASingleNewline(t *testing.T) {
	name, pipe, err := createOTPPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(pipe)
	go func() { _ = serveOTPOnce(pipe, []byte("OTP-CODE")) }()

	t.Setenv(askpassPipeEnv, name)
	stdout, restore := captureStdout(t)
	err = RunAskpass()
	restore()
	if err != nil {
		t.Fatalf("RunAskpass: %v", err)
	}
	if got := stdout(); got != "OTP-CODE\n" {
		t.Fatalf("stdout = %q, want %q", got, "OTP-CODE\n")
	}
}

func readPipe(t *testing.T, name string) string {
	t.Helper()
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(wide, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatalf("connect to %s: %v", name, err)
	}
	defer windows.CloseHandle(handle)
	buf := make([]byte, 1024)
	var read uint32
	if err := windows.ReadFile(handle, buf, &read, nil); err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(buf[:read])
}

// captureStdout redirects os.Stdout through a pipe. RunAskpass writes to it
// directly, exactly as it does when OpenSSH invokes it.
func captureStdout(t *testing.T) (func() string, func()) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	collected := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(reader)
		collected <- buf.String()
	}()
	var once bool
	restore := func() {
		if once {
			return
		}
		once = true
		os.Stdout = original
		_ = writer.Close()
	}
	t.Cleanup(restore)
	return func() string { return <-collected }, restore
}
