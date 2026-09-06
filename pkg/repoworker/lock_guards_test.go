package repoworker

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLockGuardOnlyAcceptsOrdinaryFiveArgumentInvocation(t *testing.T) {
	for _, args := range [][]string{{"repo", "path", "user", "comment", "0"}, {"repo", "path", "user", "comment", "1"}, {"repo", "path", "user", "comment", ""}, {"repo", "path", "user", "comment", "0", "extra"}, nil} {
		var stderr bytes.Buffer
		code := RunLockGuard(args, &stderr)
		allowed := len(args) == 5 && args[4] == "0"
		if (code == 0) != allowed {
			t.Fatalf("args=%q code=%d", args, code)
		}
		if allowed && stderr.Len() != 0 {
			t.Fatal("ordinary hook must stay silent")
		}
	}
}

func init() {
	if len(os.Args) == 2 && os.Args[1] == "--lock-guard-version" {
		fmt.Println(LockGuardVersion)
		os.Exit(0)
	}
	if name := filepath.Base(os.Args[0]); name == "pre-lock" || name == "pre-unlock" {
		os.Exit(RunLockGuard(os.Args[1:], os.Stderr))
	}
}

func TestLockGuardsRejectForcePreserveTokenAndPermitConditionalRelease(t *testing.T) {
	f, wc, doc := realReplacementFixture(t)
	repo := filepath.Join(f.authority.Locks.RepositoriesRoot, f.req.RepoID)
	if err := InstallLockGuards(repo, os.Args[0]); err != nil {
		t.Fatal(err)
	}
	if err := InstallLockGuards(repo, os.Args[0]); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"lock", "unlock"} {
		out, err := exec.Command("svn", command, "--force", "--username", "competitor", doc).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "forced lock replacement/release is forbidden") {
			t.Fatalf("force %s: %v %s", command, err, out)
		}
		lock, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
		if err != nil || lock == nil || lock.Token != f.req.ObservedToken {
			t.Fatalf("force changed token: %+v %v", lock, err)
		}
	}
	if err := f.authority.Prepare(t.Context(), f.session, f.req); err != nil {
		t.Fatalf("ordinary conditional release denied: %v", err)
	}
	replacementCommand(t, "svn", "lock", "--username", "new-owner", "-m", "normal", doc)
	replacementCommand(t, "svn", "unlock", "--username", "new-owner", doc)
	if data, err := os.ReadFile(doc); err != nil || string(data) != "local work" {
		t.Fatalf("changed bytes in %s: %q %v", wc, data, err)
	}
}

func TestLockGuardsPreserveForeignHooksAndResumePartialInstall(t *testing.T) {
	for _, mode := range []string{"foreign", "symlink", "partial"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _ := realReplacementFixture(t)
			repo := filepath.Join(f.authority.Locks.RepositoriesRoot, f.req.RepoID)
			p := filepath.Join(repo, "hooks", "pre-unlock")
			raw := []byte("#!/bin/sh\nexit 7\n")
			if mode == "partial" {
				if err := os.Symlink(os.Args[0], p); err != nil {
					t.Fatal(err)
				}
			} else if mode == "symlink" {
				if err := os.Symlink("pre-unlock.tmpl", p); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(p, raw, 0755); err != nil {
				t.Fatal(err)
			}
			err := InstallLockGuards(repo, os.Args[0])
			if mode == "partial" {
				if err != nil {
					t.Fatal(err)
				}
				states, err := InspectLockGuards(repo, os.Args[0])
				if err != nil {
					t.Fatal(err)
				}
				for _, state := range states {
					if state.State != "installed" {
						t.Fatal(states)
					}
				}
			} else {
				if err == nil {
					t.Fatal("foreign hook accepted")
				}
				if _, err := os.Lstat(filepath.Join(repo, "hooks", "pre-lock")); !os.IsNotExist(err) {
					t.Fatal("partial install before foreign-hook refusal")
				}
				if mode == "foreign" {
					got, _ := os.ReadFile(p)
					if string(got) != string(raw) {
						t.Fatal("foreign hook changed")
					}
				}
				if mode == "symlink" {
					if target, err := os.Readlink(p); err != nil || target != "pre-unlock.tmpl" {
						t.Fatal("symlink changed")
					}
				}
			}
		})
	}
}
