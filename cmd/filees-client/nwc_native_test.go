package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/internal/svnurl"
)

func TestNoWorkingCopyNativePublishRestoreAndReverse(t *testing.T) {
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn unavailable")
	}
	mucc, err := exec.LookPath("svnmucc")
	if err != nil {
		t.Skip("svnmucc unavailable")
	}
	admin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin unavailable")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if out, err := exec.Command(admin, "create", repo).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, out)
	}
	source := filepath.Join(root, "source")
	logDir := filepath.Join(root, "log")
	for _, dir := range []string{source, logDir, filepath.Join(source, "Faktury", "2026", "10")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	localPDF := filepath.Join(source, "Faktury", "2026", "10", "faktura.pdf")
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(localPDF, "one")
	url := svnurl.File(repo)
	cfg := Config{
		SVN: svn, SVNMucc: mucc, Identity: filepath.Join(root, "id"),
		KnownHosts: filepath.Join(root, "known_hosts"), Port: 22,
		SSH: filepath.Join(root, "ssh"), LogDir: logDir,
	}
	spec := workingCopySpec{ID: "ksefuck", Path: source, URL: url}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	remote, err := newNWCRemote(cfg, url)
	if err != nil {
		t.Fatal(err)
	}
	run := func(restore, reverse bool) {
		t.Helper()
		if err := syncWithoutWC(ctx, cfg, spec, runFlags{message: "nwc synthetic", restoreSet: restore, reverseReconcileSet: reverse}); err != nil {
			t.Fatal(err)
		}
	}
	revision := func(want int64) {
		t.Helper()
		got, err := remote.revision(ctx)
		if err != nil || got != want {
			t.Fatalf("revision=%d, want %d: %v", got, want, err)
		}
	}
	cat := func(want string) {
		t.Helper()
		out, err := exec.Command(svn, "cat", url+"/Faktury/2026/10/faktura.pdf").CombinedOutput()
		if err != nil || string(out) != want {
			t.Fatalf("repository PDF=%q, want %q: %v", out, want, err)
		}
	}
	muccDo := func(args ...string) {
		t.Helper()
		out, err := exec.Command(mucc, append([]string{"-m", "synthetic"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("svnmucc %v: %v: %s", args, err, out)
		}
	}
	run(false, false)
	revision(1)
	cat("one")
	if _, err := os.Stat(filepath.Join(source, ".svn")); !os.IsNotExist(err) {
		t.Fatalf("unexpected working copy: %v", err)
	}
	run(false, false)
	revision(1)
	write(localPDF, "two")
	run(false, false)
	revision(2)
	cat("two")
	if err := os.Remove(localPDF); err != nil {
		t.Fatal(err)
	}
	run(false, false)
	revision(2)
	cat("two")
	muccDo("rm", url+"/Faktury/2026/10/faktura.pdf")
	revision(3)
	write(localPDF, "two")
	run(false, false)
	revision(3) // a remote deletion is not undone by ordinary -nwc
	run(true, false)
	revision(4)
	cat("two")
	muccDo("rm", url+"/Faktury/2026")
	revision(5)
	run(false, false)
	revision(5) // deleting a parent also blocks implicit restore
	run(true, false)
	revision(6)
	cat("two")
	remoteOnly := filepath.Join(root, "remote.pdf")
	write(remoteOnly, "server-only")
	muccDo("put", remoteOnly, url+"/remote.pdf")
	revision(7)
	write(localPDF, "local-change")
	write(filepath.Join(source, "local-only.pdf"), "remove-me")
	run(false, true)
	revision(7)
	if data, err := os.ReadFile(localPDF); err != nil || string(data) != "two" {
		t.Fatalf("reverse PDF=%q: %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(source, "remote.pdf")); err != nil || string(data) != "server-only" {
		t.Fatalf("reverse remote-only=%q: %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(source, "local-only.pdf")); !os.IsNotExist(err) {
		t.Fatalf("local-only path survived reverse reconcile: %v", err)
	}
	// svnmucc's -r must reject an overwrite based on a revision preceding a
	// concurrent change to the same path. -nwc relies on this CAS boundary.
	write(remoteOnly, "concurrent")
	muccDo("put", remoteOnly, url+"/Faktury/2026/10/faktura.pdf")
	revision(8)
	cmd := exec.Command(mucc, "-r", "7", "-m", "stale", "put", localPDF, url+"/Faktury/2026/10/faktura.pdf")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("stale svnmucc overwrote concurrent change: %s", out)
	}
	cat("concurrent")
	write(localPDF, "concurrent")
	write(filepath.Join(source, "Faktury", "2026", "10", "faktura 2.pdf"), "with-space")
	write(filepath.Join(source, "Faktury", "2026", "10", "zażółć.pdf"), "unicode")
	run(false, false)
	revision(9)
	for rel, want := range map[string]string{
		"Faktury/2026/10/faktura 2.pdf": "with-space",
		"Faktury/2026/10/zażółć.pdf":    "unicode",
	} {
		var got bytes.Buffer
		if err := remote.cat(ctx, rel, 9, &got); err != nil || got.String() != want {
			t.Fatalf("escaped path %s=%q, want %q: %v", rel, got.String(), want, err)
		}
	}
}

func TestNoWCRejectsUnrequestedRestoreAndSelfIngest(t *testing.T) {
	if code := cmdSync([]string{"-restore-set"}); code != 2 {
		t.Fatalf("restore without -nwc exit=%d", code)
	}
	if code := cmdSync([]string{"-nwc", "-restore-set", "-reverse-reconcile-set"}); code != 2 {
		t.Fatalf("opposite directions together exit=%d", code)
	}
	root := t.TempDir()
	inside := filepath.Join(root, "log")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkNWCSource(root, inside); err == nil {
		t.Fatal("journal inside source accepted")
	}
	spool, err := prepareNWCTemp(inside, "ksefuck")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "orphan.bin"), []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareNWCTemp(inside, "ksefuck"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(spool, "orphan.bin")); !os.IsNotExist(err) {
		t.Fatalf("stale private spool survived retry: %v", err)
	}
}
