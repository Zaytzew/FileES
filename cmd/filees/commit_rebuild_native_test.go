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
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"github.com/google/uuid"
)

// Real binary conflict + mixed old WC + mismatched native owner without its
// previous transaction. All product operations use the native adapter; CLI
// SVN only creates the isolated reproduction. Nothing touches a live repo.
func TestCanonicalRecoveryRealNativeSVN(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Skip("FILEES_SVN_PROBE required for native E2E")
	}
	root := t.TempDir()
	run := func(bin string, args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		t.Log(bin, args)
		if out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v %s", bin, args, err, out)
		}
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	repository := filepath.Join(root, "repository")
	url := svnurl.File(repository)
	author, wc := filepath.Join(root, "author"), filepath.Join(root, "KRAŃCOWA-PŁOŃSK")
	run("svnadmin", "create", repository)
	run("svn", "checkout", url, author)
	write(filepath.Join(author, "plan.dwg"), "base\x00")
	run("svn", "add", filepath.Join(author, "plan.dwg"))
	run("svn", "propset", "svn:mime-type", "application/octet-stream", filepath.Join(author, "plan.dwg"))
	run("svn", "commit", author, "-m", "base")
	run("svn", "checkout", url, wc)
	write(filepath.Join(wc, "plan.dwg"), "local\x00")
	write(filepath.Join(author, "plan.dwg"), "server\x00")
	run("svn", "commit", author, "-m", "server wins")
	run("svn", "update", "--non-interactive", "--accept", "postpone", wc)
	write(filepath.Join(author, "new.txt"), "later server revision")
	run("svn", "add", filepath.Join(author, "new.txt"))
	run("svn", "commit", author, "-m", "HEAD advanced")
	write(filepath.Join(wc, "unversioned.txt"), "unsent work")
	repo := config.Repo{ID: "repo", ServerID: "lab", RepoURL: url, LocalPath: wc, Access: "rw"}
	if err := ensureWorkingCopyIdentity(wc, expectedWorkingCopyIdentity(repo.ServerID, repo.ID, url)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wc, ".filees/commit_cache"), 0700); err != nil {
		t.Fatal(err)
	}
	oldOwner, id := uuid.NewString(), uuid.NewString()
	write(filepath.Join(wc, ".svn/filees-native-writer-v1"), "filees.native-writer/v1\n"+oldOwner+"\n")
	intent := map[string]any{"schema": "filees.commit-intent/v1", "id": id, "repo_url": url, "repo_id": repo.ID, "wc": wc, "phase": "attempting", "first_revision": 2, "revision": 0, "paths": []string{"plan.dwg"}}
	if err := writeCanonicalJSON(filepath.Join(wc, ".filees/commit_cache/transaction.json"), intent); err != nil {
		t.Fatal(err)
	}
	c := client.New(client.Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "CLI-fallback-forbidden.exe")})
	if err := withCanonicalWriterLease(filepath.Join(wc, ".svn/filees-native-writer-v1"), func(*os.File) error {
		if _, err := canonicalNativeOwner(t.Context(), c, wc); err == nil {
			t.Fatal("native helper bypassed recovery writer lease")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	retired := filepath.Join(wc, ".filees-retired-test")
	if err := os.Rename(filepath.Join(wc, ".filees"), retired); err != nil {
		t.Fatal(err)
	}
	_, lateErr := canonicalNativeOwner(t.Context(), c, wc)
	if err := os.Rename(retired, filepath.Join(wc, ".filees")); err != nil {
		t.Fatal(err)
	}
	if lateErr == nil {
		t.Fatal("late native helper entered the retired live namespace")
	}
	s := &commit.Service{Cli: c, RepoURL: url, RequireSVNMetadata: true}
	s.BindWorkingCopy(repo.ID, wc)
	var job *canonicalRecoveryJob
	s.PlanCanonicalRecovery = func(ctx context.Context, transaction string, head int64) (*contract.CommitRecoveryPlan, error) {
		j, plan, err := planCanonicalWorkingCopy(ctx, repo, c, transaction, head)
		job = j
		return plan, err
	}
	s.AcceptCanonicalRecovery = func(ctx context.Context, id string) error { return acceptCanonicalWorkingCopy(ctx, repo, c, job, id) }
	plan, err := s.PlanCommitRecovery(t.Context())
	if err != nil || plan.Choice != contract.CommitRecoveryRebuild || plan.HeadRevision != 3 {
		t.Fatal(plan, err)
	}
	result, err := s.ApplyCommitRecovery(t.Context(), plan.PlanID, plan.Choice)
	if err != nil || result.State != "rebuild_requested" {
		t.Fatal(result, err)
	}
	if err := recoverCanonicalWorkingCopy(t.Context(), repo, c); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{"plan.dwg": "server\x00", "new.txt": "later server revision"} {
		b, err := os.ReadFile(filepath.Join(wc, path))
		if err != nil || string(b) != value {
			t.Fatal(path, string(b), err)
		}
	}
	for path, value := range map[string]string{"plan.dwg": "local\x00", "unversioned.txt": "unsent work"} {
		b, err := os.ReadFile(filepath.Join(wc, filepath.FromSlash(plan.ConflictCopy), path))
		if err != nil || string(b) != value {
			t.Fatal(path, string(b), err)
		}
	}
	owner, err := os.ReadFile(filepath.Join(plan.Archive, ".svn/filees-native-writer-v1"))
	if err != nil || string(owner) != "filees.native-writer/v1\n"+oldOwner+"\n" {
		t.Fatal("old native owner released or lost", string(owner), err)
	}
	if !canonicalExists(filepath.Join(plan.Archive, ".filees/commit_cache/transaction.json")) {
		t.Fatal("original intent lost")
	}
	if head, err := c.(client.TransactionCommitter).CommitHead(t.Context(), url); err != nil || head != 3 {
		t.Fatal("recovery committed", head, err)
	}
	if err := verifyCanonicalCheckout(t.Context(), c, *job, wc); err != nil {
		t.Fatal(err)
	}
	fresh := &commit.Service{Cli: c, RepoURL: url, RequireSVNMetadata: true}
	fresh.BindWorkingCopy(repo.ID, wc)
	if fresh.CommitRecoveryRequired() || commit.HasUnresolvedCommit(wc) {
		t.Fatal("new working copy retained old HOLD")
	}
}
