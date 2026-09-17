//go:build !windows

package repoworker

import (
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/svnurl"
	"github.com/google/uuid"
)

func init() {
	if filepath.Base(os.Args[0]) == realmQuotaHook {
		os.Exit(RunRealmQuotaGuard(os.Args[1:], os.Stderr))
	}
}

func TestRealmQuotaBoundsEveryRepositoryOfTheRealmTogether(t *testing.T) {
	for _, tool := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable", tool)
		}
	}
	root := t.TempDir()
	create := func() string {
		t.Helper()
		repository := filepath.Join(root, uuid.NewString())
		if out, err := exec.Command("svnadmin", "create", repository).CombinedOutput(); err != nil {
			t.Fatalf("svnadmin: %v %s", err, out)
		}
		return repository
	}
	realm, other := uuid.NewString(), uuid.NewString()
	first, second, foreign, free := create(), create(), create(), create()
	base := int64(0)
	for _, repository := range []string{first, second} {
		size, err := treeSize(repository)
		if err != nil {
			t.Fatal(err)
		}
		base += size
	}
	const headroom = 600 << 10
	for _, repository := range []string{first, second} {
		if err := InstallRealmQuota(repository, os.Args[0], realm, base+headroom); err != nil {
			t.Fatal(err)
		}
		// Installing again is a no-op; a different realm is refused.
		if err := InstallRealmQuota(repository, os.Args[0], realm, base+headroom); err != nil {
			t.Fatal(err)
		}
		if err := InstallRealmQuota(repository, os.Args[0], other, base+headroom); err == nil {
			t.Fatal("repository moved to another realm quota")
		}
	}
	if err := InstallRealmQuota(foreign, os.Args[0], other, base+headroom); err != nil {
		t.Fatal(err)
	}

	commit := func(repository string, size int) (string, error) {
		t.Helper()
		payload := filepath.Join(t.TempDir(), "payload.bin")
		data := make([]byte, size)
		if _, err := rand.Read(data); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(payload, data, 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("svn", "import", "--non-interactive", "-m", "payload", payload, svnurl.File(repository)+"/"+uuid.NewString()+".bin").CombinedOutput()
		return string(out), err
	}
	if out, err := commit(first, 250<<10); err != nil {
		t.Fatalf("commit inside the quota refused: %v %s", err, out)
	}
	if out, err := commit(second, 250<<10); err != nil {
		t.Fatalf("second repository inside the realm quota refused: %v %s", err, out)
	}
	out, err := commit(second, 250<<10)
	if err == nil || !strings.Contains(out, "demo account is limited") {
		t.Fatalf("commit over the realm quota accepted: %v %s", err, out)
	}
	// Another realm and a repository without a quota are untouched by it.
	if out, err := commit(foreign, 250<<10); err != nil {
		t.Fatalf("other realm refused by this realm's usage: %v %s", err, out)
	}
	if out, err := commit(free, 2<<20); err != nil {
		t.Fatalf("repository without quota refused: %v %s", err, out)
	}
	if used, err := RealmQuotaUsage(root, other); err != nil || used >= base+headroom {
		t.Fatalf("other realm usage=%d err=%v", used, err)
	}
}

func TestRealmQuotaHookNeverReplacesAForeignPreCommit(t *testing.T) {
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("svnadmin unavailable")
	}
	repository := filepath.Join(t.TempDir(), uuid.NewString())
	if out, err := exec.Command("svnadmin", "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin: %v %s", err, out)
	}
	hook := filepath.Join(repository, "hooks", realmQuotaHook)
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InstallRealmQuota(repository, os.Args[0], uuid.NewString(), 1<<30); err == nil || !strings.Contains(err.Error(), "explicit hook integration") {
		t.Fatalf("foreign pre-commit err=%v", err)
	}
}

func TestEffectsWithoutQuotaMarkNothing(t *testing.T) {
	if err := (ServerEffects{}).MarkRealmQuota(t.Context(), uuid.NewString(), uuid.NewString()); err != nil {
		t.Fatal(err)
	}
}
