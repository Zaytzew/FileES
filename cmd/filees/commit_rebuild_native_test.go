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
	cache := filepath.Join(wc, ".filees/commit_cache/cache.json")
	queueA := `{"rel":"plan.dwg","op":"modified"}`
	queueB := `{"rel":"unversioned.txt","op":"added"}`
	write(cache, "["+queueA+","+queueB+"]")
	plan, err := s.PlanCommitRecovery(t.Context())
	if err != nil || plan.Choice != contract.CommitRecoveryRebuild || plan.HeadRevision != 3 {
		t.Fatal(plan, err)
	}
	result, err := s.ApplyCommitRecovery(t.Context(), plan.PlanID, plan.Choice)
	if err != nil || result.State != "rebuild_requested" {
		t.Fatal(result, err)
	}
	// Quiescence may rewrite an older cache in a different order. This must
	// preserve the accepted proof even with the actual native SVN checkout.
	rewritten := "[" + queueB + "," + queueA + "]"
	write(cache, rewritten)
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
	if raw, err := os.ReadFile(filepath.Join(plan.Archive, ".filees/commit_cache/cache.json")); err != nil || string(raw) != rewritten {
		t.Fatal("original queue lost", err)
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

// A second client already committed the same removals. Retrying a failed
// deletion on the older WC must not create another failed transaction; the
// existing, explicitly approved canonical route preserves unrelated edits.
func TestCanonicalRecoveryAlreadyDeletedRealNativeSVN(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Skip("FILEES_SVN_PROBE required for native E2E")
	}
	root := t.TempDir()
	run := func(bin string, args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(t.Context(), bin, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", bin, err, out)
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
	author, wc := filepath.Join(root, "author"), filepath.Join(root, "reader")
	run("svnadmin", "create", repository)
	run("svn", "checkout", url, author)
	paths := []string{"photo.jpg", "acceptance.txt"}
	for _, path := range append(append([]string(nil), paths...), "plan.dwg") {
		write(filepath.Join(author, path), "base\x00")
	}
	run("svn", "add", filepath.Join(author, "photo.jpg"), filepath.Join(author, "acceptance.txt"), filepath.Join(author, "plan.dwg"))
	run("svn", "commit", author, "-m", "base")
	run("svn", "checkout", url, wc)
	run("svn", "delete", filepath.Join(author, paths[0]), filepath.Join(author, paths[1]))
	run("svn", "commit", author, "-m", "intentional removals")
	repo := config.Repo{ID: "repo", ServerID: "lab", RepoURL: url, LocalPath: wc, Access: "rw"}
	if err := ensureWorkingCopyIdentity(wc, expectedWorkingCopyIdentity(repo.ServerID, repo.ID, url)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wc, ".filees/commit_cache"), 0700); err != nil {
		t.Fatal(err)
	}
	c := client.New(client.Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "CLI-fallback-forbidden.exe"), Timeout: 20 * time.Second})
	if _, err := c.Delete(t.Context(), wc, paths); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(wc, "plan.dwg"), "local work must survive\x00")
	id := uuid.NewString()
	_, rev, commitErr := c.(client.TransactionCommitter).CommitWithID(t.Context(), wc, url, paths, "same removals", false, id, 3)
	if commitErr == nil || rev != 0 {
		t.Fatal("stale delete unexpectedly committed", rev, commitErr)
	}
	t.Logf("original stale-delete error: %v", commitErr)
	owner, err := c.(interface {
		InspectCommitWriter(context.Context, string) (string, error)
	}).InspectCommitWriter(t.Context(), wc)
	if err != nil || owner != id {
		t.Fatal("fixture did not retain uncertain native writer", owner, err)
	}
	intent := map[string]any{"schema": "filees.commit-intent/v1", "id": id, "repo_url": url, "repo_id": repo.ID, "wc": wc, "phase": "attempting", "first_revision": 3, "revision": 0, "paths": paths}
	if err := writeCanonicalJSON(filepath.Join(wc, ".filees/commit_cache/transaction.json"), intent); err != nil {
		t.Fatal(err)
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
	for range 3 {
		plan, err := s.PlanCommitRecovery(t.Context())
		if err != nil || plan.Choice != contract.CommitRecoveryRebuild || plan.HeadRevision != 2 {
			t.Fatalf("blind retry offered on stale deletion: %+v %v", plan, err)
		}
		if !s.CommitRecoveryRequired() {
			t.Fatal("planning retired HOLD")
		}
	}
	plan, err := s.PlanCommitRecovery(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ApplyCommitRecovery(t.Context(), plan.PlanID, plan.Choice)
	if err != nil || result.State != "rebuild_requested" {
		t.Fatal(result, err)
	}
	if err := recoverCanonicalWorkingCopy(t.Context(), repo, c); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if _, err := os.Lstat(filepath.Join(wc, path)); !os.IsNotExist(err) {
			t.Fatal("remote deletion resurrected", path, err)
		}
	}
	for path, value := range map[string]string{filepath.Join(wc, "plan.dwg"): "base\x00", filepath.Join(wc, filepath.FromSlash(plan.ConflictCopy), "plan.dwg"): "local work must survive\x00", filepath.Join(plan.Archive, "plan.dwg"): "local work must survive\x00"} {
		if b, err := os.ReadFile(path); err != nil || string(b) != value {
			t.Fatal("user bytes lost", path, err)
		}
	}
	if !canonicalExists(filepath.Join(plan.Archive, ".filees/commit_cache/transaction.json")) {
		t.Fatal("original intent lost")
	}
	if head, err := c.(client.TransactionCommitter).CommitHead(t.Context(), url); err != nil || head != 2 {
		t.Fatal("recovery published again", head, err)
	}
	fresh := &commit.Service{Cli: c, RepoURL: url, RequireSVNMetadata: true}
	fresh.BindWorkingCopy(repo.ID, wc)
	if fresh.CommitRecoveryRequired() || commit.HasUnresolvedCommit(wc) {
		t.Fatal("fresh WC retained old HOLD")
	}
	if err := recoverCanonicalWorkingCopy(t.Context(), repo, c); err != nil {
		t.Fatal("completed recovery is not restart-safe", err)
	}
}
