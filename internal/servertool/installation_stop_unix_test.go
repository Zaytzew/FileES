//go:build linux || openbsd

package servertool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/storagewatch"
	"github.com/google/uuid"
)

func TestInstallationStopRealSVNSessionDrainAndRetry(t *testing.T) {
	manager, c, grant, lease := newSupervisorTestSession(t)
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(c.Activation.Root)
	// Reuse the actual native supervisor/child sandbox fixture, not a stub.
	entry := filepath.Join(root, "client-entry")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nexec "+quote(os.Args[0])+" -test.run=^TestCapacityNativeSessionProcess$ -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c.Activation.ClientEntryPath = entry
	c.Repositories.ResultsRoot = filepath.Join(root, "results")
	if err := os.MkdirAll(c.Repositories.ResultsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(root, "fixture.json")
	raw, err := json.Marshal(capacitySessionFixture{c, grant.OperationID, grant.ClientID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCapacityNativeSessionProcess$")
	cmd.Env = append(os.Environ(), "FILEES_TEST_CAPACITY_SESSION="+fixture)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if _, err := io.ReadFull(output, make([]byte, 1)); err != nil {
		_ = cmd.Wait()
		t.Fatal(err, stderr.String())
	}
	b := storagewatch.InstallationBinding{OperationID: uuid.NewString(), PreviousManifest: strings.Repeat("a", 64), CandidateManifest: strings.Repeat("b", 64)}
	inspected := 0
	inspect := func() error {
		inspected++
		// Metadata removal is the actual supervisor ACK after child exit.
		entries, err := os.ReadDir(c.Activation.SessionRoot)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "session-") {
				return errors.New("live session at inspection")
			}
		}
		return nil
	}
	if err := withInstallationWritersAndSessionsStopped(ctx, c, b, inspect); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("native session not terminated")
	}
	if !strings.Contains(stderr.String(), "FILEES-SESSION-ENDED") {
		t.Fatal(stderr.String())
	}
	if !manager.SessionAllowed(grant.OperationID, grant.ClientID) {
		t.Fatal("activation revoked")
	}
	if l, err := manager.ClaimSession(grant.OperationID, grant.ClientID); l != nil || !errors.Is(err, storagewatch.ErrInstallationStopped) {
		t.Fatal(l, err)
	}
	if err := withInstallationWritersAndSessionsStopped(ctx, c, b, inspect); err != nil || inspected != 2 {
		t.Fatal("retry failed", err, inspected)
	}
	other := b
	other.OperationID = uuid.NewString()
	if err := withInstallationWritersAndSessionsStopped(ctx, c, other, inspect); err == nil || inspected != 2 {
		t.Fatal("different operation took over", err, inspected)
	}
}

func TestInstallationStopNoSessionAckLeavesIntent(t *testing.T) {
	_, c, _, lease := newSupervisorTestSession(t)
	defer lease.Close()
	c.Repositories.ResultsRoot = filepath.Join(filepath.Dir(c.Activation.Root), "results")
	if err := os.MkdirAll(c.Repositories.ResultsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	b := storagewatch.InstallationBinding{OperationID: uuid.NewString(), PreviousManifest: strings.Repeat("a", 64), CandidateManifest: strings.Repeat("b", 64)}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	err := withInstallationWritersAndSessionsStopped(ctx, c, b, func() error { t.Error("unacknowledged session accepted"); return nil })
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := (storagewatch.Gate{Root: c.Activation.Root}).CheckInstallationAdmission(); !errors.Is(err, storagewatch.ErrInstallationStopped) {
		t.Fatal(err)
	}
}
