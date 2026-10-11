//go:build linux || openbsd

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"filees/internal/svnurl"
)

// Limits are changed only in a child test process; the parent and other tests
// retain their caller's resource policy. These are real svnmucc commits.
func TestNoWCAtomicLargeSetWithDescriptorLimits(t *testing.T) {
	mode := os.Getenv("FILEES_TEST_LIMITS_MODE")
	if mode == "" {
		for _, program := range []string{"svn", "svnmucc", "svnadmin"} {
			if _, err := exec.LookPath(program); err != nil {
				t.Skip(program + " unavailable")
			}
		}
		for _, mode := range []string{"soft", "hard"} {
			t.Run(mode, func(t *testing.T) {
				cmd := exec.Command(os.Args[0], "-test.run=^TestNoWCAtomicLargeSetWithDescriptorLimits$", "-test.v")
				cmd.Env = append(os.Environ(), "FILEES_TEST_LIMITS_MODE="+mode)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("native descriptor limit: %v\n%s", err, out)
				}
			})
		}
		return
	}
	var limits syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limits); err != nil {
		t.Fatal(err)
	}
	limits.Cur = 128
	if mode == "hard" {
		limits.Max = 128
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limits); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	source := filepath.Join(root, "source")
	log := filepath.Join(root, "log")
	admin, _ := exec.LookPath("svnadmin")
	svn, _ := exec.LookPath("svn")
	mucc, _ := exec.LookPath("svnmucc")
	if out, err := exec.Command(admin, "create", repo).CombinedOutput(); err != nil {
		t.Fatalf("create repository: %v: %s", err, out)
	}
	for _, dir := range []string{source, log} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 200; i++ {
		if err := os.WriteFile(filepath.Join(source, fmt.Sprintf("private-invoice-%03d.pdf", i)), []byte(fmt.Sprintf("synthetic %d", i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{SVN: svn, SVNMucc: mucc, Identity: filepath.Join(root, "id"), KnownHosts: filepath.Join(root, "known"), Port: 22, LogDir: log}
	spec := workingCopySpec{ID: "large-set", Path: source, URL: svnurl.File(repo)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var progress bytes.Buffer
	err := syncWithoutWC(ctx, cfg, spec, runFlags{message: "large atomic fixture", progress: &progress})
	remote, remoteErr := newNWCRemote(cfg, spec.URL)
	if remoteErr != nil {
		t.Fatal(remoteErr)
	}
	rev, revErr := remote.revision(ctx)
	if revErr != nil {
		t.Fatal(revErr)
	}
	if mode == "hard" {
		if err == nil || !strings.Contains(err.Error(), "nothing committed") || rev != 0 {
			t.Fatalf("partial or missing refusal: rev=%d err=%v", rev, err)
		}
	} else {
		if err != nil || rev != 1 {
			t.Fatalf("not one atomic commit: rev=%d err=%v", rev, err)
		}
		if !strings.Contains(progress.String(), "checked=200/200") || !strings.Contains(progress.String(), "committing 200 files atomically") || strings.Contains(progress.String(), "private-invoice") {
			t.Fatalf("bad progress: %s", progress.String())
		}
		if err := syncWithoutWC(ctx, cfg, spec, runFlags{message: "retry"}); err != nil {
			t.Fatal(err)
		}
		if rev, err := remote.revision(ctx); err != nil || rev != 1 {
			t.Fatalf("retry committed again: rev=%d err=%v", rev, err)
		}
	}
	if entries, err := os.ReadDir(source); err != nil || len(entries) != 200 {
		t.Fatalf("source changed: files=%d err=%v", len(entries), err)
	}
}
