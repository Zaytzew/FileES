//go:build native_svn_probe && windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/internal/svnurl"
	"filees/pkg/client"
	control "filees/pkg/control/v1"
	"filees/pkg/provisioning"
	"github.com/google/uuid"
)

func TestNativeLifecycleWithoutCLI(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	ctx := context.Background()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	svnadmin := os.Getenv("FILEES_PROBE_SVNADMIN")
	if svnadmin == "" {
		svnadmin = "svnadmin"
	}
	if out, err := exec.Command(svnadmin, "create", repo).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	url := svnurl.File(repo)
	wc := filepath.Join(root, "import")
	if err := os.MkdirAll(filepath.Join(wc, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wc, "folder", "new.txt"), []byte("before checkout"), 0600); err != nil {
		t.Fatal(err)
	}
	c := client.New(client.Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "absent-svn.exe")})
	expected := expectedWorkingCopyIdentity("lab", "repo", url)
	svn := lifecycleSVN{Client: c, expected: func(string, string) (workingCopyIdentity, error) { return expected, nil }}
	store, err := provisioning.NewStore(filepath.Join(root, "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	opID, reqID := uuid.NewString(), uuid.NewString()
	if _, err := store.CreateValidated(opID, "lab-client", wc, "Native import"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequestRepository(opID, reqID); err != nil {
		t.Fatal(err)
	}
	result, err := control.NewSuccessResult(opID, reqID, control.TicketCreateRepository, control.CreateRepositoryResult{RepoID: expected.RepoID, RepoURL: url}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRepositoryResult(result); err != nil {
		t.Fatal(err)
	}
	op, err := provisioning.PublishInitialSnapshot(ctx, store, opID, uuid.NewString(), svn, provisioning.ImportLimits{MaxBatchFiles: 100, MaxBatchBytes: 1 << 20})
	if err != nil || op.Revision != 1 || op.Paths != 2 {
		t.Fatal("initial import", op, err)
	}
	if _, err := svn.Checkout(ctx, url, wc); err != nil {
		t.Fatal("resume", err)
	}
	if n, err := c.Revision(ctx, url); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	serviceWC := filepath.Join(root, "service-wc")
	updater := serviceProjectionUpdater{client: c, url: url + "/", prepare: serviceWCPreparation(c, "lab", "client", url+"/")}
	if _, err := updater.Update(ctx, serviceWC); err != nil {
		t.Fatal("service checkout", err)
	}
	if _, err := updater.Update(ctx, serviceWC); err != nil {
		t.Fatal("service update", err)
	}
	if _, err := updater.Cleanup(ctx, serviceWC); err != nil {
		t.Fatal("service cleanup", err)
	}
	got, err := os.ReadFile(filepath.Join(serviceWC, "folder", "new.txt"))
	if err != nil || string(got) != "before checkout" {
		t.Fatal(string(got), err)
	}
	adopt := filepath.Join(root, "adopt")
	if _, err := c.Checkout(ctx, url, adopt); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Status(ctx, adopt, nil); err != nil {
		t.Fatal("inspection", err)
	}
	if _, err := os.Stat(filepath.Join(adopt, ".filees")); !os.IsNotExist(err) {
		t.Fatal("inspection stamped WC", err)
	}
	if err := prepareLifecycleWC(ctx, c, adopt, expected); err != nil {
		t.Fatal("adopt", err)
	}
	if _, err := c.Update(ctx, adopt); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Checkout(ctx, url+"/foreign", adopt); err == nil {
		t.Fatal("foreign resume accepted")
	}
}
