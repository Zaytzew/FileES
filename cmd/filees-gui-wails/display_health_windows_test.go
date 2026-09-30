//go:build windows

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDisplayBrowserChild(t *testing.T) {
	if os.Getenv("FILEES_TEST_BROWSER_CHILD") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString("ready\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestDisplayBrowserNativeObservation(t *testing.T) {
	// An isolated child, not the user's browser or live FileES window.
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	exe := filepath.Join(t.TempDir(), "msedgewebview2.exe")
	output, err := os.Create(exe)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(output, input)
	output.Close()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(exe, "-test.run=^TestDisplayBrowserChild$")
	child.Env = append(os.Environ(), "FILEES_TEST_BROWSER_CHILD=1")
	child.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	ready := make([]byte, 6)
	if _, err := io.ReadFull(stdout, ready); err != nil {
		t.Fatal(err)
	}
	tracker := &browserProcesses{}
	defer tracker.close()
	present, _, err := tracker.observe(uint32(os.Getpid()))
	if err != nil || !present || len(tracker.handles) != 1 {
		t.Fatalf("child not observed: %v %v", present, err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	stdin.Close()
	present, exit, err := tracker.observe(uint32(os.Getpid()))
	if err != nil || present || !strings.Contains(exit, "exit=0x") || len(tracker.handles) != 0 {
		t.Fatalf("loss not observed or handle leaked: %v %s %v", present, exit, err)
	}
	// Unrelated browsers owned by other applications are not our browser.
	present, _, err = tracker.observe(0)
	if err != nil || present {
		t.Fatalf("unrelated browser counted: %v %v", present, err)
	}
}
