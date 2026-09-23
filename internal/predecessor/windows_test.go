//go:build windows

package predecessor

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// Removing "the FileES MSI" is only safe while the code we ask Windows
// Installer about is the one the installer is actually built with.
func TestMSIUpgradeCodeMatchesTheInstaller(t *testing.T) {
	wxs, err := os.ReadFile(filepath.Join("..", "..", "packaging", "windows", "filees.wxs"))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`UpgradeCode="([^"]+)"`).FindSubmatch(wxs)
	if match == nil {
		t.Fatal("filees.wxs declares no UpgradeCode")
	}
	if want := "{" + string(match[1]) + "}"; want != MSIUpgradeCode {
		t.Fatalf("MSIUpgradeCode = %s, installer uses %s", MSIUpgradeCode, want)
	}
}

func TestMSIPresentLooksForTheDaemonNotTheDirectory(t *testing.T) {
	local := t.TempDir()
	dir, err := MSIInstallDir(local)
	if err != nil {
		t.Fatal(err)
	}
	// What an uninstall leaves behind: config and logs, no program.
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if present, err := MSIPresent(local); err != nil || present {
		t.Fatalf("leftover config reported as an installation: present=%v err=%v", present, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "filees.exe"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if present, err := MSIPresent(local); err != nil || !present {
		t.Fatalf("installed daemon not seen: present=%v err=%v", present, err)
	}
	if _, err := MSIPresent("relative"); err == nil {
		t.Fatal("relative LOCALAPPDATA accepted")
	}
}

// Read-only against the real Windows Installer database: whatever is
// installed here, every answer must be a product code, never a guess.
func TestMSIProductCodesReturnsProductCodes(t *testing.T) {
	codes, err := MSIProductCodes()
	if err != nil {
		t.Fatal(err)
	}
	guid := regexp.MustCompile(`^\{[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}\}$`)
	for _, code := range codes {
		if !guid.MatchString(code) {
			t.Fatalf("not a product code: %q", code)
		}
	}
}

func TestUninstallMSIRefusesAnythingButAProductCode(t *testing.T) {
	for _, code := range []string{"", "FileES", "/x", "{9E4A1F52-8C1D-4E63-9F0A-6B7D2C5A81E4} /qb"} {
		if err := UninstallMSI(context.Background(), code); err == nil {
			t.Fatalf("%q accepted", code)
		}
	}
}

// The other variant's window and supervisor are ended by where their image
// lives. Anything elsewhere - here, a ping started from System32 - must live.
func TestTerminateUnderEndsOnlyProcessesFromThatDirectory(t *testing.T) {
	system := systemDirectory()
	ping := filepath.Join(system, "PING.EXE")
	dir := t.TempDir()
	copyFile(t, ping, filepath.Join(dir, "ping-copy.exe"))

	inside := exec.Command(filepath.Join(dir, "ping-copy.exe"), "-n", "60", "127.0.0.1")
	outside := exec.Command(ping, "-n", "60", "127.0.0.1")
	for _, cmd := range []*exec.Cmd{inside, outside} {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = outside.Process.Kill(); _ = inside.Process.Kill() })

	terminated, err := TerminateUnder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if terminated != 1 {
		t.Fatalf("terminated %d processes, want exactly the one from %s", terminated, dir)
	}
	done := make(chan error, 1)
	go func() { done <- inside.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("process from the directory is still running")
	}
	outsideDone := make(chan error, 1)
	go func() { outsideDone <- outside.Wait() }()
	select {
	case <-outsideDone:
		t.Fatal("process outside the directory was ended")
	case <-time.After(500 * time.Millisecond):
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
