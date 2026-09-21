package commit

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"github.com/google/uuid"
)

type claimClient struct {
	client.Client
	observation  client.LockObservation
	observations int
	updates      int
	dirty        bool
	afterUpdate  func()
}

func (c *claimClient) ReadLockObservation(context.Context, string, string) (client.LockObservation, error) {
	c.observations++
	return c.observation, nil
}
func (c *claimClient) UpdateDepthEmpty(context.Context, string, []string) (string, error) {
	c.updates++
	if c.afterUpdate != nil {
		c.afterUpdate()
	}
	return "", nil
}
func (c *claimClient) Status(_ context.Context, _ string, paths []string) ([]client.StatusEntry, error) {
	item := "normal"
	if c.dirty {
		item = "modified"
	}
	return []client.StatusEntry{{Path: paths[0], Item: item}}, nil
}

func claimFixture(t *testing.T) (*Service, *claimClient, string, contract.LockReleaseRequest) {
	t.Helper()
	wc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wc, ".filees", "state"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wc, "file.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &claimClient{}
	s := &Service{Cli: c, RepoURL: "svn://example/repo"}
	r := contract.LockReleaseRequest{RequestID: uuid.NewString(), ServerID: "office", RepoID: "repo", Role: "requester", State: "accepted", Path: "file.txt", ObservedLockID: "old"}
	return s, c, wc, r
}

func TestClaimWaitsForUnlockThenUpdatesAndAcquiresOnceAcrossRestart(t *testing.T) {
	s, c, wc, r := claimFixture(t)
	c.observation.Remote = &client.LockInfo{Token: "old"}
	calls := 0
	acquire := func(context.Context, []string) (string, error) {
		calls++
		if c.updates != 1 {
			t.Fatal("acquisition before update")
		}
		c.observation = client.LockObservation{Local: &client.LockInfo{Token: "new"}, Remote: &client.LockInfo{Token: "new"}}
		return "", nil
	}
	if done, err := s.ClaimReleasedLock(t.Context(), "repo", wc, r, acquire); done || err != nil || c.updates != 0 || calls != 0 {
		t.Fatalf("before unlock: %v %v updates=%d calls=%d", done, err, c.updates, calls)
	}
	c.observation.Remote = nil
	if done, err := s.ClaimReleasedLock(t.Context(), "repo", wc, r, acquire); !done || err != nil {
		t.Fatal(done, err)
	}
	// User releases it. A restarted daemon reading the same consent must not lock again.
	c.observation = client.LockObservation{}
	restarted := &Service{Cli: c, RepoURL: s.RepoURL}
	if done, err := restarted.ClaimReleasedLock(t.Context(), "repo", wc, r, acquire); !done || err != nil || calls != 1 || c.updates != 1 {
		t.Fatalf("consent replay: %v %v updates=%d calls=%d", done, err, c.updates, calls)
	}
}

func TestClaimNeverFollowsAnotherLockOrUsesNonConsent(t *testing.T) {
	for _, scenario := range []string{"pending", "dismissed", "expired", "lock_gone", "stale", "holder", "other-repo", "replacement", "race", "dirty", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			s, c, wc, r := claimFixture(t)
			switch scenario {
			case "holder":
				r.Role = "holder"
			case "other-repo":
				r.RepoID = "other"
			case "replacement":
				c.observation.Remote = &client.LockInfo{Token: "new-owner"}
			case "race":
				c.afterUpdate = func() { c.observation.Remote = &client.LockInfo{Token: "new-owner"} }
			case "dirty":
				c.dirty = true
			case "symlink":
				outside := filepath.Join(t.TempDir(), "target")
				os.WriteFile(outside, []byte("private"), 0600)
				os.Remove(filepath.Join(wc, r.Path))
				if err := os.Symlink(outside, filepath.Join(wc, r.Path)); err != nil {
					t.Skip(err)
				}
			default:
				r.State = scenario
			}
			_, _ = s.ClaimReleasedLock(t.Context(), "repo", wc, r, func(context.Context, []string) (string, error) { t.Fatal("unexpected acquisition"); return "", nil })
			if scenario != "race" && c.updates != 0 {
				t.Fatal("non-consent or unsafe path updated")
			}
		})
	}
}

func TestClaimLostReplyObservesWithoutSecondAcquisition(t *testing.T) {
	for _, tookLock := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-proof", true: "own-lock"}[tookLock], func(t *testing.T) {
			s, c, wc, r := claimFixture(t)
			calls := 0
			acquire := func(context.Context, []string) (string, error) {
				calls++
				if tookLock {
					c.observation = client.LockObservation{Local: &client.LockInfo{Token: "mine"}, Remote: &client.LockInfo{Token: "mine"}}
				}
				return "", errors.New("lost reply")
			}
			if _, err := s.ClaimReleasedLock(t.Context(), "repo", wc, r, acquire); err == nil {
				t.Fatal("failure hidden")
			}
			restarted := &Service{Cli: c, RepoURL: s.RepoURL}
			done, err := restarted.ClaimReleasedLock(t.Context(), "repo", wc, r, acquire)
			if !done || calls != 1 || (tookLock && err != nil) || (!tookLock && err == nil) {
				t.Fatalf("recovery: done=%v err=%v calls=%d", done, err, calls)
			}
			c.observation = client.LockObservation{}
			_, _ = restarted.ClaimReleasedLock(t.Context(), "repo", wc, r, acquire)
			if calls != 1 {
				t.Fatal("reacquired after user release")
			}
		})
	}
}

func TestClaimReleasedLockRealSVN(t *testing.T) {
	for _, tool := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(err)
		}
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	owner := filepath.Join(root, "owner")
	requester := filepath.Join(root, "requester")
	run := func(dir, tool string, args ...string) {
		t.Helper()
		cmd := exec.Command(tool, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v %s", tool, args, err, out)
		}
	}
	run(root, "svnadmin", "create", repository)
	repoPath := filepath.ToSlash(repository)
	if filepath.VolumeName(repository) != "" {
		repoPath = "/" + repoPath
	}
	repoURL := (&url.URL{Scheme: "file", Path: repoPath}).String()
	run(root, "svn", "checkout", repoURL, owner)
	os.WriteFile(filepath.Join(owner, "file.txt"), []byte("old"), 0600)
	run(owner, "svn", "add", "file.txt")
	run(owner, "svn", "propset", "svn:needs-lock", "*", "file.txt")
	run(owner, "svn", "commit", "-m", "seed")
	run(root, "svn", "checkout", repoURL, requester)
	run(owner, "svn", "lock", "file.txt")
	os.WriteFile(filepath.Join(owner, "file.txt"), []byte("new head"), 0600)
	run(owner, "svn", "commit", "--no-unlock", "-m", "holder changes")
	c := client.New(client.Options{SvnPath: "svn", Timeout: 30 * time.Second})
	observation, err := c.(client.LockObservationReader).ReadLockObservation(t.Context(), requester, filepath.Join(requester, "file.txt"))
	if err != nil || observation.Remote == nil {
		t.Fatal(observation, err)
	}
	if err := os.MkdirAll(filepath.Join(requester, ".filees", "state"), 0700); err != nil {
		t.Fatal(err)
	}
	r := contract.LockReleaseRequest{RequestID: uuid.NewString(), ServerID: "office", RepoID: "repo", Role: "requester", State: "accepted", Path: "file.txt", ObservedLockID: observation.Remote.Token}
	s := &Service{Cli: c, RepoURL: repoURL}
	acquire := func(ctx context.Context, paths []string) (string, error) { return c.Lock(ctx, requester, paths) }
	if done, err := s.ClaimReleasedLock(t.Context(), "repo", requester, r, acquire); done || err != nil {
		t.Fatal("claimed before holder unlock", done, err)
	}
	run(owner, "svn", "unlock", "file.txt")
	if done, err := s.ClaimReleasedLock(t.Context(), "repo", requester, r, acquire); !done || err != nil {
		t.Fatal("claim failed", done, err)
	}
	raw, _ := os.ReadFile(filepath.Join(requester, "file.txt"))
	if string(raw) != "new head" {
		t.Fatal("claimed stale file", string(raw))
	}
	run(requester, "svn", "unlock", "file.txt")
	restarted := &Service{Cli: c, RepoURL: repoURL}
	if _, err := restarted.ClaimReleasedLock(t.Context(), "repo", requester, r, acquire); err != nil {
		t.Fatal(err)
	}
	observation, err = c.(client.LockObservationReader).ReadLockObservation(t.Context(), requester, filepath.Join(requester, "file.txt"))
	if err != nil || observation.Remote != nil {
		t.Fatal("consent replayed", observation, err)
	}
}
