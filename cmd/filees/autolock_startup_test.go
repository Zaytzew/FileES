package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientview"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/reposupervisor"
	"filees/pkg/talk"
)

func TestReadWriteRecoveryFailureDoesNotAuthorizeLocalRW(t *testing.T) {
	wc := t.TempDir()
	if err := os.Mkdir(filepath.Join(wc, ".svn"), 0755); err != nil {
		t.Fatal(err)
	}
	fake := &recoveryClient{updateErr: errors.New("update failed")}
	if recoverReadWriteWorkingCopy(t.Context(), fake, wc, &commit.Service{Logger: talk.With("recovery-test")}, nil, talk.With("recovery-test")) {
		t.Fatal("failed update authorized local RW")
	}
	if recoverReadWriteWorkingCopy(t.Context(), fake, t.TempDir(), &commit.Service{}, nil, talk.With("recovery-test")) {
		t.Fatal("missing SVN metadata authorized local RW")
	}
}

func TestAutolockStartupRealSVN(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file URL fixture; Windows acceptance remains separate")
	}
	for _, bin := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s unavailable", bin)
		}
	}
	run := func(name string, args ...string) {
		t.Helper()
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
	}
	for _, tc := range []struct {
		name, realm string
		preexisting bool
		writable    bool
	}{
		{"owner-after-checkout", "owner", true, true},
		{"owner-after-migration", "owner", false, true},
		{"guest-remains-manual", "guest", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			repository := filepath.Join(root, "repository")
			wc := filepath.Join(root, "wc")
			url := "file://" + repository
			run("svnadmin", "create", repository)
			run("svn", "checkout", url, wc)
			doc := filepath.Join(wc, "doc.txt")
			if err := os.WriteFile(doc, []byte("document"), 0644); err != nil {
				t.Fatal(err)
			}
			run("svn", "add", doc)
			if tc.preexisting {
				run("svn", "propset", "svn:needs-lock", "*", doc)
			}
			run("svn", "commit", "-m", "fixture", wc)
			cli := client.New(client.Options{Timeout: 10 * time.Second})
			server := ipcserver.New(filepath.Join(root, "sock"))
			state := server.RegisterRepoAccess("docs", url, wc, "lab", contract.AccessReadWrite)
			repo := config.Repo{ID: "docs", RepoURL: url, LocalPath: wc, Access: contract.AccessReadWrite, RealmID: tc.realm, OwnerRealmID: "owner", EditPassports: true, WatchInterval: time.Hour, PollInterval: time.Hour}
			instance, err := startReadWrite(t.Context(), repoRuntime{config: repo, state: state}, cli, reposupervisor.Desired{Key: reposupervisor.Key{ServerID: "lab", RepoID: "docs"}}, readWriteDependencies{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := instance.Stop(t.Context()); err != nil {
					t.Error(err)
				}
			}()
			info, err := os.Stat(doc)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm()&0200 != 0; got != tc.writable {
				t.Fatalf("writable=%v, want %v", got, tc.writable)
			}
			if lock, err := cli.LockInfo(t.Context(), wc, doc); err != nil || lock != nil {
				t.Fatalf("startup acquired lock: %+v, %v", lock, err)
			}
			props, err := cli.PropList(t.Context(), wc, "svn:needs-lock")
			if err != nil || !props["doc.txt"] {
				t.Fatalf("migration property = %v, %v", props, err)
			}
			if policy := readAppliedEditingPolicy(filepath.Join(wc, ".filees", "state")); policy != clientview.EditingLockRequired {
				t.Fatalf("migration receipt = %q", policy)
			}
		})
	}
}
