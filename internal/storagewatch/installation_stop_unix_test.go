//go:build linux || openbsd

package storagewatch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func installationTestBinding() InstallationBinding {
	return InstallationBinding{uuid.NewString(), strings.Repeat("a", 64), strings.Repeat("b", 64)}
}

func TestInstallationStopBindingAndCapacityIndependence(t *testing.T) {
	g := Gate{Root: t.TempDir()}
	b := installationTestBinding()
	if err := g.WithInstallationWritersStopped(t.Context(), b, func() error { t.Fatal("no stop"); return nil }); err == nil {
		t.Fatal("drain accepted without durable intent")
	}
	if err := g.RequestInstallationStop(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(g.installationStopPath())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(g.installationStopPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := g.RequestInstallationStop(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	for _, other := range []InstallationBinding{
		{uuid.NewString(), b.PreviousManifest, b.CandidateManifest},
		{b.OperationID, strings.Repeat("c", 64), b.CandidateManifest},
		{b.OperationID, b.PreviousManifest, strings.Repeat("c", 64)},
	} {
		if err := g.RequestInstallationStop(t.Context(), other); err == nil {
			t.Fatal("different binding accepted")
		}
		if err := g.WithInstallationWritersStopped(t.Context(), other, func() error { t.Fatal("foreign drain"); return nil }); err == nil {
			t.Fatal("foreign drain accepted")
		}
	}
	// Capacity recovery has its own authority. It cannot resume installation.
	if err := g.WithExclusive(t.Context(), g.Resume); err != nil {
		t.Fatal(err)
	}
	if err := g.WithWriter(t.Context(), func() error { t.Fatal("writer admitted"); return nil }); !errors.Is(err, ErrInstallationStopped) {
		t.Fatal(err)
	}
	if err := g.WithInstallationWritersStopped(t.Context(), b, func() error { return errors.New("inspection failed") }); err == nil {
		t.Fatal("error swallowed")
	}
	after, err := os.ReadFile(g.installationStopPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("intent modified", err)
	}
	final, err := os.Stat(g.installationStopPath())
	if err != nil || !os.SameFile(info, final) {
		t.Fatal("intent inode replaced", err)
	}
	if err := (Gate{Root: g.Root}).CheckInstallationAdmission(); !errors.Is(err, ErrInstallationStopped) {
		t.Fatal("fresh reader admitted", err)
	}
}

func TestInstallationStopPublishesDuringWriterAndWaitsForAck(t *testing.T) {
	g := Gate{Root: t.TempDir()}
	b := installationTestBinding()
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- g.WithWriter(t.Context(), func() error { close(entered); <-release; return nil }) }()
	<-entered
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	if err := g.RequestInstallationStop(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 70*time.Millisecond)
	err := g.WithInstallationWritersStopped(ctx, b, func() error { t.Error("drained active writer"); return nil })
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := g.WithInstallationWritersStopped(t.Context(), b, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := g.CheckInstallationAdmission(); !errors.Is(err, ErrInstallationStopped) {
		t.Fatal("drain opened admission", err)
	}
}

func TestInstallationStopRechecksQueuedWriter(t *testing.T) {
	g := Gate{Root: t.TempDir()}
	locked, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- g.WithExclusive(t.Context(), func() error { close(locked); <-release; return nil }) }()
	<-locked
	writer := make(chan error, 1)
	go func() {
		writer <- g.WithWriter(t.Context(), func() error { t.Error("queued writer entered"); return nil })
	}()
	if err := g.RequestInstallationStop(t.Context(), installationTestBinding()); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-writer; !errors.Is(err, ErrInstallationStopped) {
		t.Fatal(err)
	}
}

func TestInstallationStopSerializesCompetingPlans(t *testing.T) {
	g := Gate{Root: t.TempDir()}
	type result struct {
		binding InstallationBinding
		err     error
	}
	results := make(chan result, 6)
	start := make(chan struct{})
	for range 6 {
		b := installationTestBinding()
		go func() { <-start; results <- result{b, g.RequestInstallationStop(t.Context(), b)} }()
	}
	close(start)
	var winner InstallationBinding
	winners := 0
	for range 6 {
		r := <-results
		if r.err == nil {
			winners++
			winner = r.binding
		}
	}
	if winners != 1 {
		t.Fatal("competing plans admitted", winners)
	}
	stop, err := g.InstallationStop()
	if err != nil || stop == nil || stop.InstallationBinding != winner {
		t.Fatal(stop, err)
	}
}

func TestInstallationStopInvalidBindingHasNoEffects(t *testing.T) {
	valid := installationTestBinding()
	for _, b := range []InstallationBinding{
		{"", valid.PreviousManifest, valid.CandidateManifest},
		{uuid.Nil.String(), valid.PreviousManifest, valid.CandidateManifest},
		{valid.OperationID, "short", valid.CandidateManifest},
		{valid.OperationID, valid.PreviousManifest, strings.ToUpper(valid.CandidateManifest)},
	} {
		g := Gate{Root: t.TempDir()}
		if err := g.RequestInstallationStop(t.Context(), b); err == nil {
			t.Fatal("invalid binding accepted")
		}
		entries, err := os.ReadDir(g.Root)
		if err != nil || len(entries) != 0 {
			t.Fatal("invalid request created metadata", err, entries)
		}
	}
}

func TestInstallationStopRejectsUnfinishedWriterAndInvalidMetadata(t *testing.T) {
	t.Run("orphan", func(t *testing.T) {
		g := Gate{Root: t.TempDir()}
		func() {
			defer func() { _ = recover() }()
			_ = g.WithWriter(t.Context(), func() error { panic("leave unacknowledged marker") })
		}()
		b := installationTestBinding()
		if err := g.RequestInstallationStop(t.Context(), b); err != nil {
			t.Fatal(err)
		}
		if err := g.WithInstallationWritersStopped(t.Context(), b, func() error { t.Error("orphan acknowledged"); return nil }); !errors.Is(err, ErrUnfinishedWriter) {
			t.Fatal(err)
		}
	})
	for _, raw := range []string{"", "null", "{}", "{}{}", strings.Repeat("x", 4097)} {
		t.Run("invalid", func(t *testing.T) {
			g := Gate{Root: t.TempDir()}
			if err := os.WriteFile(g.installationStopPath(), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if err := g.CheckInstallationAdmission(); err == nil {
				t.Fatal("invalid stop admitted")
			}
			if err := g.RequestInstallationStop(t.Context(), installationTestBinding()); err == nil {
				t.Fatal("invalid stop overwritten")
			}
		})
	}
	t.Run("alias", func(t *testing.T) {
		g := Gate{Root: t.TempDir()}
		if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), g.installationStopPath()); err != nil {
			t.Fatal(err)
		}
		if err := g.CheckInstallationAdmission(); err == nil {
			t.Fatal("alias admitted")
		}
	})
	t.Run("cancel", func(t *testing.T) {
		g := Gate{Root: t.TempDir()}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := g.RequestInstallationStop(ctx, installationTestBinding()); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if err := g.CheckInstallationAdmission(); err != nil {
			t.Fatal("cancel created stop", err)
		}
	})
}

func TestInstallationStopKilledRequester(t *testing.T) {
	if root := os.Getenv("FILEES_TEST_INSTALLATION_STOP_ROOT"); root != "" {
		binding := InstallationBinding{os.Getenv("FILEES_TEST_INSTALLATION_ID"), strings.Repeat("a", 64), strings.Repeat("b", 64)}
		if err := (Gate{Root: root}).RequestInstallationStop(t.Context(), binding); err != nil {
			t.Fatal(err)
		}
		_, _ = os.Stdout.WriteString("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	g := Gate{Root: t.TempDir()}
	b := installationTestBinding()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInstallationStopKilledRequester$")
	cmd.Env = append(os.Environ(), "FILEES_TEST_INSTALLATION_STOP_ROOT="+g.Root, "FILEES_TEST_INSTALLATION_ID="+b.OperationID)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ready := make([]byte, 5)
	if _, err := io.ReadFull(output, ready); err != nil || string(ready) != "ready" {
		t.Fatal("child checkpoint", err, string(ready))
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("requester not killed")
	}
	if err := (Gate{Root: g.Root}).RequestInstallationStop(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if err := g.CheckInstallationAdmission(); !errors.Is(err, ErrInstallationStopped) {
		t.Fatal(err)
	}
}
