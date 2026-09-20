package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"filees/internal/svnrotate"
)

func TestRotationSandbox(t *testing.T) {
	if runtime.GOOS != "openbsd" {
		t.Skip("native pledge/unveil")
	}
	if os.Getenv("FILEES_ROTATE_SANDBOX_TEST") == "" {
		root := t.TempDir()
		child := exec.Command(os.Args[0], "-test.run=^TestRotationSandbox$", "-test.v")
		child.Env = append(os.Environ(), "FILEES_ROTATE_SANDBOX_TEST="+root)
		if out, err := child.CombinedOutput(); err != nil {
			t.Fatalf("sandbox child: %v\n%s", err, out)
		}
		return
	}
	root := os.Getenv("FILEES_ROTATE_SANDBOX_TEST")
	repo, archive := filepath.Join(root, "repos", "project"), filepath.Join(root, "archive")
	if err := os.MkdirAll(filepath.Dir(repo), 0750); err != nil {
		t.Fatal(err)
	}
	run := func(name string, args ...string) {
		t.Helper()
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", name, err, out)
		}
	}
	run("svnadmin", "create", repo)
	run("svn", "mkdir", "file://"+filepath.ToSlash(repo)+"/tree", "-m", "initial")
	if err := confineRotation(repo, archive); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile("/etc/passwd"); err == nil {
		t.Fatal("foreign file visible")
	}
	cfg := svnrotate.Config{RepoPath: repo, ArchiveDir: archive, SizeThreshold: 1, MaxAge: 24 * time.Hour, DumpDepth: 1}
	if err := svnrotate.Rotate(cfg, "sandbox test", io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, found, err := svnrotate.Recover(repo, archive, ""); err != nil || found {
		t.Fatalf("recovery after completion: %v %v", found, err)
	}
	run("svnlook", "tree", repo)
}
