//go:build !windows

package servertool

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"filees/internal/obsandbox"
	"filees/internal/svnurl"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

// Run under the real owner, both ordinary and root-owned private fixtures.
// No injected UID or fabricated admin claim is accepted.
func TestArchiveReleaseActualOwnerAdapter(t *testing.T) {
	f := newRealmRemovalE2EFixture(t, 1)
	c := serverconfig.Config{Activation: f.activationConfig}
	c.Repositories.Root = f.repositoriesRoot
	c.Repositories.ResultsRoot = f.resultsRoot
	c.Repositories.DataAuthzFile = f.publisher.DataAuthzFile
	c.Repositories.SVNAdminBinary = f.svnadmin
	look, err := exec.LookPath("svnlook")
	if err != nil {
		t.Skip("svnlook unavailable")
	}
	c.Repositories.SVNLookBinary = look
	worker := os.Getenv("FILEES_ARCHIVE_WORKER")
	if worker == "" {
		t.Skip("actual archive worker required")
	}
	repo := f.ownedRepos[0].RepoID
	raw, err := exec.CommandContext(t.Context(), look, "uuid", filepath.Join(f.repositoriesRoot, repo)).Output()
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(raw))
	frozen, err := freezeRepositoryArchive(t.Context(), c, worker, f.targetRealm, repo, uuid.NewString(), id, 1)
	if err != nil {
		t.Fatal(err)
	}
	req := repoworker.ArchiveReleaseRequest{RepoID: repo, OperationID: uuid.NewString(), ExpectedOperationID: frozen.OperationID, ExpectedEpoch: frozen.Epoch, RepositoryUUID: id, Revision: 1, Reason: "isolated real service-owner acceptance"}
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
		t.Fatal("release session did not start")
	}
	got, err := releaseRepositoryArchive(t.Context(), c, worker, req)
	if err != nil || got.State != "released" || got.Release.Actor != fmt.Sprintf("local-service-owner:uid:%d", os.Geteuid()) {
		t.Fatal(got, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("release acknowledged before session exit")
	}
	if err := repoworker.CheckCanonicalRepositoryWrite(c.Activation.ServiceWorkingCopy, repo); err != nil {
		t.Fatal(err)
	}
	realmRemovalE2ERun(t, f.svn, "mkdir", "-m", "after root release", svnurl.File(filepath.Join(f.repositoriesRoot, repo))+"/after-release")
	if got, err := releaseRepositoryArchive(t.Context(), c, worker, req); err != nil || got.State != "released" {
		t.Fatal("lost administrative reply", got, err)
	}
}

func TestArchiveReleaseRootCannotWriteForeignOwnedAuthority(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("real root required to create a differently owned fixture")
	}
	wc := filepath.Join(t.TempDir(), "service-wc")
	if err := os.Mkdir(wc, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(wc, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(wc)
	if err != nil {
		t.Fatal(err)
	}
	c := serverconfig.Config{}
	c.Activation.ServiceWorkingCopy = wc
	if _, err := releaseRepositoryArchive(t.Context(), c, "", repoworker.ArchiveReleaseRequest{}); err == nil || !strings.Contains(err.Error(), "service-state owner") {
		t.Fatal("root bypassed authority owner", err)
	}
	after, err := os.Lstat(wc)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode() != after.Mode() || before.Sys().(*syscall.Stat_t).Uid != after.Sys().(*syscall.Stat_t).Uid {
		t.Fatal("refusal changed authority ownership")
	}
	entries, err := os.ReadDir(wc)
	if err != nil || len(entries) != 0 {
		t.Fatal("wrote before owner admission", entries, err)
	}
}
