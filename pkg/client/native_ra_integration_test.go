//go:build native_svn_probe && windows

package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/svnurl"
)

func TestNativeRecoveryAdapterWithoutCLI(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	repo, wc, interrupted := filepath.Join(root, "repo"), filepath.Join(root, "wc"), filepath.Join(root, "interrupted")
	if out, err := exec.Command(nativeFixtureTool(t, "svnadmin"), "create", repo).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	url := svnurl.File(repo)
	c := New(Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "absent-cli.exe")}).(*execClient)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := c.Checkout(t.Context(), url, wc)
	must(err)
	must(os.Mkdir(filepath.Join(wc, ".filees"), 0700))
	must(os.WriteFile(filepath.Join(wc, "new.txt"), []byte("published\n"), 0600))
	_, err = c.Add(t.Context(), wc, []string{"new.txt"})
	must(err)
	must(os.CopyFS(interrupted, os.DirFS(wc)))
	marker := "882e3df8-6093-4600-8d26-6e9d09352706"
	_, rev, err := c.CommitWithID(t.Context(), wc, url, []string{"new.txt"}, "receipt", false, marker, 1)
	must(err)
	if rev != 1 {
		t.Fatal(rev)
	}
	must(os.WriteFile(filepath.Join(interrupted, "new.txt"), []byte("later unsent edit\n"), 0600))
	for range 2 {
		must(c.ReconcileCommit(t.Context(), interrupted, url, []string{"new.txt"}, marker, rev))
	}
	entries, err := c.Status(t.Context(), interrupted, []string{"new.txt"})
	must(err)
	if len(entries) != 1 || entries[0].Item != "modified" {
		t.Fatal(entries)
	}
	got, err := os.ReadFile(filepath.Join(interrupted, "new.txt"))
	must(err)
	if string(got) != "later unsent edit\n" {
		t.Fatal("later text lost", string(got))
	}
	head, err := c.CommitHead(t.Context(), url)
	must(err)
	if head != rev {
		t.Fatal("recovery created a new revision", head)
	}
}

func TestNativeRAAdapterWithoutCLI(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if out, e := exec.Command(nativeFixtureTool(t, "svnadmin"), "create", repo).CombinedOutput(); e != nil {
		t.Fatalf("%v %s", e, out)
	}
	url := svnurl.File(repo)
	wc := filepath.Join(root, "author")
	b := filepath.Join(root, "reader")
	c := New(Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "absent-cli.exe")}).(*execClient)
	ctx := context.Background()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	write := func(p, s string) { t.Helper(); must(os.WriteFile(p, []byte(s), 0600)) }
	_, e := c.Checkout(ctx, url, wc)
	must(e)
	// No .filees yet: inspection must not invoke the deliberately absent CLI.
	_, e = c.GetInfo(ctx, wc)
	must(e)
	if _, e = os.Stat(filepath.Join(wc, ".filees")); !os.IsNotExist(e) {
		t.Fatal("inspection stamped WC", e)
	}
	if head, e := c.Revision(ctx, url); e != nil || head != 0 {
		t.Fatal(head, e)
	}
	must(os.Mkdir(filepath.Join(wc, ".filees"), 0700))
	write(filepath.Join(wc, "old-新.txt"), "first\n")
	_, e = c.Add(ctx, wc, []string{"old-新.txt"})
	must(e)
	_, rev, e := c.CommitWithRevision(ctx, wc, url, []string{"old-新.txt"}, "ogłoszenie 新", false)
	must(e)
	if rev != 1 {
		t.Fatal(rev)
	}
	_, e = c.Checkout(ctx, url, b)
	must(e)
	must(os.Mkdir(filepath.Join(b, ".filees"), 0700))
	_, e = c.LockWithComment(ctx, wc, []string{"old-新.txt"}, "observed", false)
	must(e)
	observation, e := c.ReadLockObservation(ctx, b, "old-新.txt")
	must(e)
	if observation.Local != nil || observation.Remote == nil {
		t.Fatal(observation)
	}
	lockConfirmed, e := c.ConfirmLock(ctx, wc, "old-新.txt", "observed")
	must(e)
	if lockConfirmed == nil {
		t.Fatal("owner receipt missing")
	}
	lockConfirmed, e = c.ConfirmLock(ctx, b, "old-新.txt", "observed")
	must(e)
	if lockConfirmed != nil {
		t.Fatal("reader claimed owner receipt")
	}
	locks, e := c.ListLocks(ctx, b)
	must(e)
	if len(locks) != 1 || locks[0].Comment != "observed" {
		t.Fatal(locks)
	}
	lock, e := c.LockInfo(ctx, b, "old-新.txt")
	must(e)
	if lock == nil || lock.Token != locks[0].Token {
		t.Fatal(lock)
	}
	_, e = c.Unlock(ctx, wc, []string{"old-新.txt"})
	must(e)
	lock, e = c.LockInfo(ctx, b, "old-新.txt")
	must(e)
	if lock != nil {
		t.Fatal("released lock still reported", lock)
	}
	info, e := c.GetInfo(ctx, b)
	must(e)
	if !strings.Contains(info, "Repository UUID:") {
		t.Fatal(info)
	}
	n, e := c.Revision(ctx, b)
	must(e)
	if n != 1 {
		t.Fatal(n)
	}
	messages, e := c.LogMessages(ctx, b, 1, 1)
	must(e)
	if len(messages) != 1 || messages[0].Message != "ogłoszenie 新" {
		t.Fatal(messages)
	}
	must(os.Rename(filepath.Join(wc, "old-新.txt"), filepath.Join(wc, "renamed-新.txt")))
	write(filepath.Join(wc, "renamed-新.txt"), "edited rename\n")
	_, e = c.RecordMove(ctx, wc, "old-新.txt", "renamed-新.txt")
	must(e)
	_, rev, e = c.CommitWithRevision(ctx, wc, url, []string{"old-新.txt", "renamed-新.txt"}, "move", false)
	must(e)
	if rev != 2 {
		t.Fatal(rev)
	}
	confirmed, e := c.VerifyCommittedMove(ctx, wc, "old-新.txt", "renamed-新.txt")
	must(e)
	if !confirmed {
		t.Fatal("move history unconfirmed")
	}
	update, e := c.Update(ctx, b)
	must(e)
	changes, valid := UpdateChanges(update)
	if !valid || changes["old-新.txt"] != "D" || changes["renamed-新.txt"] != "A" {
		t.Fatalf("missing incoming move notifications: %s", update)
	}
	got, e := os.ReadFile(filepath.Join(b, "renamed-新.txt"))
	must(e)
	if string(got) != "edited rename\n" {
		t.Fatal(string(got))
	}
	write(filepath.Join(wc, "renamed-新.txt"), "new remote text\n")
	_, _, e = c.CommitWithRevision(ctx, wc, url, []string{"renamed-新.txt"}, "edit", false)
	must(e)
	update, e = c.Update(ctx, b)
	must(e)
	changes, valid = UpdateChanges(update)
	if !valid || changes["renamed-新.txt"] != "U" {
		t.Fatalf("missing incoming edit: %s", update)
	}
	write(filepath.Join(b, "renamed-新.txt"), "local competing text\n")
	write(filepath.Join(wc, "renamed-新.txt"), "remote competing text\n")
	_, _, e = c.CommitWithRevision(ctx, wc, url, []string{"renamed-新.txt"}, "conflicting edit", false)
	must(e)
	update, e = c.Update(ctx, b)
	must(e)
	conflicts, valid := UpdateConflicts(update)
	changes, _ = UpdateChanges(update)
	if !valid || len(conflicts) != 1 || conflicts[0] != "renamed-新.txt" || changes["renamed-新.txt"] != "" {
		t.Fatalf("conflict misreported as received: %s", update)
	}
	_, e = c.Lock(ctx, wc, []string{"renamed-新.txt"})
	must(e)
	if _, e = c.Lock(ctx, b, []string{"renamed-新.txt"}); e == nil {
		t.Fatal("contested lock accepted")
	}
	_, e = c.Unlock(ctx, wc, []string{"renamed-新.txt"})
	must(e)
	_, rev, e = c.CommitWithRevision(ctx, wc, url, []string{"renamed-新.txt"}, "no-op", false)
	must(e)
	if rev != 0 {
		t.Fatal(rev)
	}
	dst := filepath.Join(root, "cat.txt")
	must(c.CatTo(ctx, url+"/renamed-%E6%96%B0.txt", dst))
	got, e = os.ReadFile(dst)
	must(e)
	if string(got) != "remote competing text\n" {
		t.Fatal(string(got))
	}
}

func TestNativeClientCannotEnterCLIRunner(t *testing.T) {
	root := t.TempDir()
	c := New(Options{NativeSVNPath: filepath.Join(root, "absent-helper.exe"), SvnPath: filepath.Join(root, "absent-cli.exe")}).(*execClient)
	if _, err := c.run(t.Context(), root, []string{"unrouted"}); err == nil || !strings.Contains(err.Error(), "routing gap") {
		t.Fatal("CLI runner was not fenced", err)
	}
}

func TestNativeRARootConflictCannotDisappear(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	repo, a, b := filepath.Join(root, "repo"), filepath.Join(root, "a"), filepath.Join(root, "b")
	run := func(tool string, args ...string) {
		t.Helper()
		if out, err := exec.Command(nativeFixtureTool(t, tool), args...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	run("svnadmin", "create", repo)
	run("svn", "checkout", svnurl.File(repo), a)
	run("svn", "checkout", svnurl.File(repo), b)
	run("svn", "propset", "test:root", "remote", a)
	run("svn", "commit", a, "-m", "root property")
	run("svn", "propset", "test:root", "local", b)
	if err := os.Mkdir(filepath.Join(b, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	c := New(Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "absent-cli.exe")})
	if out, err := c.Update(t.Context(), b); err == nil || !strings.Contains(err.Error(), "conflict path") {
		t.Fatalf("root conflict hidden: %s %v", out, err)
	}
}
