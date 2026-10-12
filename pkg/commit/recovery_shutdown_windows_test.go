package commit

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"

	"filees/pkg/client"
	"filees/pkg/errmap"
)

func TestRecoveryShutdownExitProcess(t *testing.T) {
	if os.Getenv("FILEES_TEST_RECOVERY_SHUTDOWN_EXIT") == "1" {
		os.Exit(0xc000026b)
	}
}

func TestShutdownCompletedInspectionDefersWithoutHold(t *testing.T) {
	s, base, wc := conflictRecoveryFixture(t)
	in, err := s.readIntent(wc)
	if err != nil {
		t.Fatal(err)
	}
	in.Phase = "done"
	if err := s.writeIntent(wc, in); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(transactionPath(wc))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryShutdownExitProcess$")
	cmd.Env = append(os.Environ(), "FILEES_TEST_RECOVERY_SHUTDOWN_EXIT=1")
	launchErr := cmd.Run()
	if launchErr == nil {
		t.Fatal("fixture should fail")
	}
	fault := &client.NativeFailure{Verb: "--version", Exit: launchErr}
	c := &writerRecoveryClient{conflictRecoveryClient: base, inspectErr: fault}
	s.Cli = c
	var log bytes.Buffer
	s.ErrSink = errmap.NewSink(&log, "commit:fixture")
	found, err := s.recoverCommit(t.Context(), wc)
	if !found || !errors.Is(err, launchErr) {
		t.Fatal("lost pending inspection", found, err)
	}
	s.recordCommitFailure("caller", err)
	after, readErr := os.ReadFile(transactionPath(wc))
	if readErr != nil || !bytes.Equal(before, after) || log.Len() != 0 || c.mutations != 0 {
		t.Fatal("shutdown fabricated HOLD or changed data", readErr, log.String())
	}
	c.inspectErr = nil
	if _, err := s.recoverCommit(t.Context(), wc); err != nil {
		t.Fatal(err)
	}
	if c.mutations != 0 {
		t.Fatal("publication retried")
	}
}
