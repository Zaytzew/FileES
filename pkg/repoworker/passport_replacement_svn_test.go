package repoworker

import (
	"context"
	"filees/internal/svnurl"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/passport"
	"github.com/google/uuid"
)

func replacementCommand(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, out)
	}
	return string(out)
}

func realReplacementFixture(t *testing.T) (*replacementFixture, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix file:// fixture, not Windows runtime acceptance")
	}
	admin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin unavailable")
	}
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn unavailable")
	}
	f := newReplacementFixture(t)
	f.authority.Locks.SVNAdmin = admin
	f.authority.Locks.Run = nil
	repo := filepath.Join(f.authority.Locks.RepositoriesRoot, f.req.RepoID)
	replacementCommand(t, admin, "create", repo)
	wc := filepath.Join(t.TempDir(), "wc")
	replacementCommand(t, "svn", "checkout", svnurl.File(repo), wc)
	doc := filepath.Join(wc, filepath.FromSlash(f.req.Path))
	if err := os.MkdirAll(filepath.Dir(doc), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, []byte("local work"), 0644); err != nil {
		t.Fatal(err)
	}
	replacementCommand(t, "svn", "add", filepath.Dir(doc))
	replacementCommand(t, "svn", "propset", "svn:needs-lock", "*", doc)
	replacementCommand(t, "svn", "commit", "-m", "fixture", wc)
	comment := filepath.Join(t.TempDir(), "comment")
	if err := os.WriteFile(comment, []byte(passport.FormatComment(f.metadata)), 0600); err != nil {
		t.Fatal(err)
	}
	replacementCommand(t, admin, "lock", repo, "/"+f.req.Path, f.holder, comment, f.req.ObservedToken)
	return f, wc, doc
}

func TestPassportReplacementConditionalUnlockRealSVN(t *testing.T) {
	for _, scenario := range []string{"success", "stale-token", "wrong-holder", "replacement-before-unlock", "replacement-in-pre-unlock", "hook-refusal"} {
		t.Run(scenario, func(t *testing.T) {
			f, _, doc := realReplacementFixture(t)
			repo := filepath.Join(f.authority.Locks.RepositoriesRoot, f.req.RepoID)
			newcomer := uuid.NewString()
			switch scenario {
			case "stale-token":
				f.req.ObservedToken = "opaquelocktoken:" + uuid.NewString()
			case "wrong-holder":
				if err := f.authority.Locks.unlockIfCurrent(t.Context(), f.req.RepoID, f.req.Path, newcomer, f.req.ObservedToken); err == nil {
					t.Fatal("wrong holder unlocked")
				}
			case "replacement-before-unlock":
				f.authority.Locks.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
					if args[0] == "unlock" {
						replacementCommand(t, "svn", "lock", "--force", "--username", newcomer, "-m", "new lock", doc)
					}
					return runLockAuthorityCommand(ctx, name, args...)
				}
			case "replacement-in-pre-unlock", "hook-refusal":
				hook := "#!/bin/sh\nexit 1\n"
				if scenario == "replacement-in-pre-unlock" {
					svn, _ := exec.LookPath("svn")
					quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
					hook = "#!/bin/sh\nexec " + quote(svn) + " lock --force --username " + quote(newcomer) + " -m newer " + quote(svnurl.File(repo)+"/"+f.req.Path) + "\n"
				}
				if err := os.WriteFile(filepath.Join(repo, "hooks", "pre-unlock"), []byte(hook), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "wrong-holder" {
				err := f.authority.Prepare(t.Context(), f.session, f.req)
				if (err == nil) != (scenario == "success") {
					t.Fatalf("prepare: %v", err)
				}
			}
			// Read independently of the injected interleaving.
			f.authority.Locks.Run = nil
			lock, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "success" {
				if lock != nil {
					t.Fatalf("lock survived successful release: %+v", lock)
				}
				if err := f.authority.Prepare(t.Context(), f.session, f.req); err == nil {
					t.Fatal("replay was treated as a new authorization")
				}
			} else {
				if lock == nil {
					t.Fatal("refused operation removed lock")
				}
				if strings.HasPrefix(scenario, "replacement-") {
					if lock.Owner != newcomer || lock.Token == f.req.ObservedToken {
						t.Fatal("stale release damaged newer lock")
					}
				} else if lock.Owner != f.holder {
					t.Fatal("refusal changed lock owner")
				}
			}
			contents, err := os.ReadFile(doc)
			if err != nil || string(contents) != "local work" {
				t.Fatalf("local data changed: %q %v", contents, err)
			}
		})
	}
}

// The wrapper only selects an authenticated username for a local file://
// fixture. All locks, WC token writes and observations still run real SVN.
type replacementSVNClient struct {
	client.Client
	username   string
	forceCalls int
}

func (c *replacementSVNClient) LockWithComment(ctx context.Context, wc string, paths []string, comment string, force bool) (string, error) {
	args := []string{"lock", "--username", c.username, "--non-interactive", "--no-auth-cache", "-m", comment}
	if force {
		c.forceCalls++
		args = append(args, "--force")
	}
	args = append(args, "--")
	args = append(args, paths...)
	command := exec.CommandContext(ctx, "svn", args...)
	command.Dir = wc
	out, err := command.CombinedOutput()
	return string(out), err
}

func TestConditionalBackendMigrationAndCompetingAcquireRealSVN(t *testing.T) {
	for _, race := range []bool{false, true} {
		name := "migration"
		if race {
			name = "other-writer-wins-gap"
		}
		t.Run(name, func(t *testing.T) {
			f, wc, doc := realReplacementFixture(t)
			oldHolder := f.holder
			f.session.ClientID = uuid.NewString()
			f.client(t, f.session.ClientID, f.owner, "active")
			f.req.Mode = "migrate"
			f.req.PassportID, f.req.InstanceUID = uuid.NewString(), uuid.NewString()
			cli := &replacementSVNClient{Client: client.New(client.Options{Timeout: 10 * time.Second}), username: f.session.ClientID}
			newcomer := uuid.NewString()
			backend := passport.ConditionalSVNBackend{SVNBackend: passport.SVNBackend{Client: cli, WC: wc}}
			backend.PrepareReplacement = func(ctx context.Context, path string, meta passport.Metadata) error {
				if path != doc || meta.PreviousToken != f.req.ObservedToken {
					t.Fatal("replacement scope changed")
				}
				if err := f.authority.Prepare(ctx, f.session, f.req); err != nil {
					return err
				}
				if race {
					replacementCommand(t, "svn", "lock", "--username", newcomer, "-m", "winner", doc)
				}
				return nil
			}
			metadata := f.metadata
			metadata.PassportID, metadata.InstanceUID, metadata.PreviousToken = f.req.PassportID, f.req.InstanceUID, f.req.ObservedToken
			lock, _, err := backend.Lock(t.Context(), doc, passport.FormatComment(metadata), true)
			if (err == nil) == race {
				t.Fatalf("migration race=%v: %v", race, err)
			}
			if cli.forceCalls != 0 {
				t.Fatal("conditional backend fell back to force")
			}
			current, inspectErr := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
			if inspectErr != nil || current == nil {
				t.Fatalf("missing current lock: %v", inspectErr)
			}
			if race {
				if current.Owner != newcomer {
					t.Fatal("losing migration stole winner lock")
				}
			} else {
				if lock == nil || current.Owner != f.session.ClientID || current.Token != lock.Token || current.Token == f.req.ObservedToken {
					t.Fatal("replacement did not create new owner/token")
				}
				wcInfo := replacementCommand(t, "svn", "info", "--xml", doc)
				if !strings.Contains(wcInfo, current.Token) {
					t.Fatal("replacement token was not stored in WC")
				}
			}
			if err := f.authority.Locks.unlockIfCurrent(t.Context(), f.req.RepoID, f.req.Path, oldHolder, f.req.ObservedToken); err == nil {
				t.Fatal("old instance released new token")
			}
			contents, err := os.ReadFile(doc)
			if err != nil || string(contents) != "local work" {
				t.Fatal("migration changed local contents")
			}
		})
	}
}
