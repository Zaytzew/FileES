//go:build !windows

package servertool

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/obsandbox"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func TestArchiveFreezeRealSessionBarrierAndCanonicalPublication(t *testing.T) {
	f := newRealmRemovalE2EFixture(t, 1)
	c := serverconfig.Config{Activation: f.activationConfig}
	c.Repositories.Root = f.repositoriesRoot
	c.Repositories.ResultsRoot = f.resultsRoot
	c.Repositories.DataAuthzFile = f.publisher.DataAuthzFile
	c.Repositories.SVNAdminBinary = f.svnadmin
	svnlook, err := exec.LookPath("svnlook")
	if err != nil {
		t.Skip("svnlook unavailable")
	}
	c.Repositories.SVNLookBinary = svnlook
	worker := os.Getenv("FILEES_ARCHIVE_WORKER")
	if worker == "" {
		t.Skip("actual archive worker build required")
	}
	repo := f.ownedRepos[0].RepoID
	uuidRaw, err := exec.CommandContext(t.Context(), svnlook, "uuid", filepath.Join(f.repositoriesRoot, repo)).CombinedOutput()
	if err != nil {
		t.Fatalf("uuid: %v %s", err, uuidRaw)
	}
	nativeID := strings.TrimSpace(string(uuidRaw))
	lease, err := f.manager.ClaimSession(f.targetClients[0].OperationID, f.targetClients[0].ClientID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	original := sandboxApply
	sandboxApply = func(obsandbox.Profile) error { return nil }
	defer func() { sandboxApply = original }()
	input, writer := io.Pipe()
	defer writer.Close()
	ready := make(sessionReadyWriter, 1)
	done := make(chan error, 1)
	go func() {
		_, err := runSessionSupervisor(c, lease.Metadata.ClientID, c.Activation.ServiceRepository, f.manager, lease, input, ready, io.Discard, sessionRelayTestChild)
		closeErr := lease.Close()
		if err == nil {
			err = closeErr
		}
		done <- err
	}()
	go func() { _, _ = writer.Write([]byte("READY\n")) }()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("session did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if _, err := freezeRepositoryArchive(ctx, c, worker, f.otherRealm, repo, uuid.NewString(), nativeID, 1); err == nil {
		t.Fatal("foreign realm admitted")
	}
	got, err := freezeRepositoryArchive(ctx, c, worker, f.targetRealm, repo, uuid.NewString(), nativeID, 1)
	if err != nil || got.State != "archived" {
		t.Fatal(got, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("archive completed before supervisor acknowledged exit")
	}
	if err := repoworker.CheckCanonicalRepositoryWrite(c.Activation.ServiceWorkingCopy, repo); err == nil {
		t.Fatal("canonical mutation admission opened")
	}
	if held, err := (repoworker.ArchiveAdmissionFence{Root: c.Activation.Root}).Held(); held || err != nil {
		t.Fatal(held, err)
	}
	var diag bytes.Buffer
	called := false
	if code := runCapacityWriter(c, &diag, func() int { called = true; return ExitOK }); code != ExitOK || !called {
		t.Fatal("unrelated writes remained globally stopped", code, diag.String())
	}
	plan := got
	plan.OperationID = uuid.NewString()
	fence := repoworker.ArchiveAdmissionFence{Root: c.Activation.Root}
	if err := fence.Hold(ctx, repo, plan); err != nil {
		t.Fatal(err)
	}
	called = false
	if code := runCapacityWriter(c, &diag, func() int { called = true; return ExitOK }); code != ExitTempFail || called {
		t.Fatal("pending intent admitted server writer", code, called)
	}
}
