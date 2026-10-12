package client

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

func TestShutdownExitProcess(t *testing.T) {
	if os.Getenv("FILEES_TEST_SHUTDOWN_EXIT") == "1" {
		os.Exit(0xc000026b)
	}
}

func TestShutdownInterruptionUsesExactTypedExit(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestShutdownExitProcess$")
	cmd.Env = append(os.Environ(), "FILEES_TEST_SHUTDOWN_EXIT=1")
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || uint32(exit.ExitCode()) != 0xc000026b {
		t.Fatal("fixture did not return the Windows exit", err)
	}
	fault := &NativeFailure{Verb: "--version", Exit: err}
	if !IsShutdownInterruption(fmt.Errorf("inspect: %w", fault)) {
		t.Fatal("typed shutdown was not recognized")
	}
	for _, other := range []error{err, errors.New("exit status 0xc000026b"), &NativeFailure{Exit: err, Truncated: true}, &NativeFailure{Exit: err, Entries: []NativeErrorEntry{{Code: 155004}}}} {
		if IsShutdownInterruption(other) {
			t.Fatal("independent failure suppressed", other)
		}
	}
}
