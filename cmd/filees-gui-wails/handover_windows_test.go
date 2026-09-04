//go:build windows

package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The restarted client must outlive nothing and wait for the one it replaces.
//
// Without the wait, the tray's "Uruchom FileES ponownie" ends in silence: the
// replacement starts while its predecessor is still alive, the single-instance
// lock refuses it, and the owner is left with no interface and no message.
func TestTheReplacementWaitsForThePredecessorToExit(t *testing.T) {
	predecessor := exec.Command(os.Args[0], "-test.run=TestHelperOutlivesItsParentBriefly")
	predecessor.Env = append(os.Environ(), "FILEES_TEST_HELPER=1")
	if err := predecessor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(handoverEnv, strconv.Itoa(predecessor.Process.Pid))

	started := time.Now()
	if err := awaitReplacedInstance(); err != nil {
		t.Fatalf("awaitReplacedInstance: %v", err)
	}
	waited := time.Since(started)
	_ = predecessor.Wait()

	if waited < 300*time.Millisecond {
		t.Fatalf("returned after %v while the predecessor was still running; the restart would be refused by its own lock", waited)
	}
}

// A pid that is already gone must not cost the restart a single second.
func TestAVanishedPredecessorIsNotWaitedFor(t *testing.T) {
	finished := exec.Command(os.Args[0], "-test.run=TestHelperExitsImmediately")
	finished.Env = append(os.Environ(), "FILEES_TEST_HELPER=1")
	if err := finished.Run(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(handoverEnv, strconv.Itoa(finished.Process.Pid))

	started := time.Now()
	if err := awaitReplacedInstance(); err != nil {
		t.Fatalf("awaitReplacedInstance: %v", err)
	}
	if waited := time.Since(started); waited > 2*time.Second {
		t.Fatalf("waited %v for a process that had already exited", waited)
	}
}

// And the variable must not survive into the next restart, which would make a
// later replacement wait on whoever happens to hold that pid by then.
func TestTheHandoverIsConsumedOnce(t *testing.T) {
	t.Setenv(handoverEnv, "1")
	if err := awaitReplacedInstance(); err != nil {
		t.Fatalf("awaitReplacedInstance: %v", err)
	}
	if _, present := os.LookupEnv(handoverEnv); present {
		t.Error("the handover pid is still in the environment and would be inherited by the next restart")
	}
}

// The restart has to name itself, or the replacement has nothing to wait for.
func TestTheRestartTellsTheReplacementWhomItReplaces(t *testing.T) {
	raw, err := os.ReadFile("restart_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	// The identifier, not its value: the source names the constant.
	if !strings.Contains(source, "handoverEnv+") || !strings.Contains(source, "os.Getpid()") {
		t.Error("the restart does not pass its own pid; the replacement would take the lock before this process released it")
	}
}

func TestHelperOutlivesItsParentBriefly(t *testing.T) {
	if os.Getenv("FILEES_TEST_HELPER") != "1" {
		t.Skip("helper process")
	}
	time.Sleep(700 * time.Millisecond)
}

func TestHelperExitsImmediately(t *testing.T) {
	if os.Getenv("FILEES_TEST_HELPER") != "1" {
		t.Skip("helper process")
	}
}
