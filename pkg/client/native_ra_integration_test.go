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

func TestNativeRAAdapterWithoutCLI(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if out, e := exec.Command("svnadmin", "create", repo).CombinedOutput(); e != nil {
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

func TestNativeRARootConflictCannotDisappear(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	repo, a, b := filepath.Join(root, "repo"), filepath.Join(root, "a"), filepath.Join(root, "b")
	run := func(tool string, args ...string) {
		t.Helper()
		if out, err := exec.Command(tool, args...).CombinedOutput(); err != nil {
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
