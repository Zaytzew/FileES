package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/passport"
	"filees/pkg/reposupervisor"
	"filees/pkg/talk"
	"github.com/google/uuid"
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
		{"pending-after-restart", "owner", true, false},
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
			profile := passportFixtureProfile(t, "lab")
			repoID := uuid.NewString()
			server := ipcserver.New(filepath.Join(root, "sock"))
			state := server.RegisterRepoAccess(repoID, url, wc, "lab", contract.AccessReadWrite)
			repo := config.Repo{ID: repoID, ServerID: "lab", RepoURL: url, LocalPath: wc, Access: contract.AccessReadWrite, RealmID: tc.realm, OwnerRealmID: "owner", EditPassports: true, WatchInterval: time.Hour, PollInterval: time.Hour}
			var lost *startupLostLockClient
			if tc.name == "pending-after-restart" {
				lost = &startupLostLockClient{Client: cli, username: profile.ClientID}
				cli = lost
				stateDir := filepath.Join(wc, ".filees", "state")
				if err := os.MkdirAll(stateDir, 0700); err != nil {
					t.Fatal(err)
				}
				instanceID := loadOrCreateUUID(filepath.Join(stateDir, "client.uuid"))
				backend, err := newControlPassportBackend(repo, cli, func(string) (clientprofile.Profile, bool) { return profile, true })
				if err != nil {
					t.Fatal(err)
				}
				m, err := passport.Open(filepath.Join(wc, ".filees", "passports", "passports.json"), instanceID, backend, passport.Config{})
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := m.Acquire(t.Context(), []string{doc}, ""); err == nil {
					t.Fatal("lost reply acknowledged")
				}
			}
			deps := readWriteDependencies{passportBackend: func(repo config.Repo, svn client.Client) (passport.Backend, error) {
				return newControlPassportBackend(repo, svn, func(string) (clientprofile.Profile, bool) { return profile, true })
			}}
			deps.pathOwnership = func(ctx context.Context, repo config.Repo, cli client.Client) (passport.OwnershipView, error) {
				v := passport.OwnershipView{Owners: map[string]string{"doc.txt": "owner"}, Holds: map[string]passport.OwnershipHold{}}
				lock, err := cli.LockInfo(ctx, wc, doc)
				if err != nil {
					return v, err
				}
				if lock != nil {
					v.Holds["doc.txt"] = passport.OwnershipHold{Token: lock.Token, RealmID: "owner"}
				}
				return v, nil
			}
			instance, err := startReadWrite(t.Context(), repoRuntime{config: repo, state: state}, cli, reposupervisor.Desired{Key: reposupervisor.Key{ServerID: "lab", RepoID: repoID}}, deps)
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
			if lock, err := cli.LockInfo(t.Context(), wc, doc); err != nil || (lock != nil) != (lost != nil) {
				t.Fatalf("startup acquired lock: %+v, %v", lock, err)
			}
			if lost != nil {
				snap := state.Snapshot()
				if len(snap.PassportIssues) != 1 || snap.State != contract.StateInteractionRequired || lost.locks != 1 {
					t.Fatalf("starter lost pending: %+v", snap)
				}
				if _, err := state.Lock(t.Context(), []string{doc}); err != nil {
					t.Fatal(err)
				}
				if len(state.Snapshot().PassportIssues) != 0 || lost.locks != 1 {
					t.Fatal("receipt recovery repeated lock or kept issue")
				}
				info, err := os.Stat(doc)
				if err != nil || info.Mode().Perm()&0200 == 0 {
					t.Fatalf("confirmed passport did not restore RW: %v", err)
				}
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

// Only file:// fixture authentication and reply loss differ from production;
// SVN itself writes and verifies the local/repository fencing token.
type startupLostLockClient struct {
	client.Client
	username string
	locks    int
}

func (c *startupLostLockClient) LockWithComment(ctx context.Context, wc string, paths []string, comment string, force bool) (string, error) {
	if force {
		panic("starter selected force")
	}
	c.locks++
	args := append([]string{"lock", "--username", c.username, "-m", comment, "--"}, paths...)
	cmd := exec.CommandContext(ctx, "svn", args...)
	cmd.Dir = wc
	out, err := cmd.CombinedOutput()
	if err == nil {
		err = errors.New("fixture lost lock reply")
	}
	return string(out), err
}
func (c *startupLostLockClient) ConfirmLock(ctx context.Context, wc, path, comment string) (*client.LockInfo, error) {
	return c.Client.(client.LockReceiptReader).ConfirmLock(ctx, wc, path, comment)
}
func (c *startupLostLockClient) Unlock(ctx context.Context, wc string, paths []string) (string, error) {
	args := append([]string{"unlock", "--username", c.username, "--"}, paths...)
	cmd := exec.CommandContext(ctx, "svn", args...)
	cmd.Dir = wc
	out, err := cmd.CombinedOutput()
	return string(out), err
}
