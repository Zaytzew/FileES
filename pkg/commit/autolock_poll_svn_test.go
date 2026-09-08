package commit

import (
	"filees/internal/svnurl"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/passport"
	"filees/pkg/talk"
)

func TestAutolockPollRestoresRWAfterQuietGraceWithoutNewRevisionRealSVN(t *testing.T) {
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
	root := t.TempDir()
	repository, wc := filepath.Join(root, "repository"), filepath.Join(root, "wc")
	url := svnurl.File(repository)
	run("svnadmin", "create", repository)
	run("svn", "checkout", url, wc)
	for _, name := range []string{"doc.txt", "append-only.txt", "private.txt"} {
		path := filepath.Join(wc, name)
		if err := os.WriteFile(path, []byte("initial"), 0644); err != nil {
			t.Fatal(err)
		}
		run("svn", "add", path)
		if name != "private.txt" {
			run("svn", "propset", "svn:needs-lock", "*", path)
		}
	}
	run("svn", "propset", passport.AppendOnlyProperty, "true", filepath.Join(wc, "append-only.txt"))
	run("svn", "commit", "-m", "fixture", wc)
	run("svn", "update", wc)
	private := filepath.Join(wc, "private.txt")
	if err := os.Chmod(private, 0444); err != nil {
		t.Fatal(err)
	}
	cli := client.New(client.Options{Timeout: 10 * time.Second})
	now := time.Now()
	m, err := passport.Open(filepath.Join(root, "passports.json"), "owner-instance", passport.SVNBackend{Client: cli, WC: wc}, passport.Config{Now: func() time.Time { return now }, CloseGrace: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	doc := filepath.Join(wc, "doc.txt")
	checkRW := func(path string, want bool) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm()&0200 != 0; got != want {
			t.Fatalf("%s writable=%v, want %v", path, got, want)
		}
	}
	s := &Service{Cli: cli, RepoURL: url, RealmID: "owner", OwnerRealmID: "owner", AutoUnlockOwned: m.AutoUnlockOwned, Logger: talk.With("autolock-real-poll")}
	headPath := filepath.Join(wc, ".filees", "state", "head.rev")
	s.pollOnce(ctx, wc, headPath)
	checkRW(doc, true)
	checkRW(private, false)
	checkRW(filepath.Join(wc, "append-only.txt"), false)
	if lock, err := cli.LockInfo(ctx, wc, doc); err != nil || lock != nil {
		t.Fatalf("local RW created server lock: %+v, %v", lock, err)
	}
	if _, _, err := m.Acquire(ctx, []string{doc}, "owner"); err != nil {
		t.Fatal(err)
	}
	finish, err := m.BeginPublish(ctx, []string{doc})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, []byte("published"), 0644); err != nil {
		finish()
		t.Fatal(err)
	}
	_, err = cli.CommitKeepLocks(ctx, wc, []string{doc}, "publish")
	finish()
	if err != nil {
		t.Fatal(err)
	}
	m.MarkPublished([]string{doc})
	run("svn", "update", wc)
	before, err := cli.Revision(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if err := m.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	checkRW(doc, false)
	remote, err := cli.Revision(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	local, err := cli.Revision(ctx, wc)
	if err != nil {
		t.Fatal(err)
	}
	if remote != before || local != remote {
		t.Fatalf("fixture not at unchanged HEAD: before=%d local=%d remote=%d", before, local, remote)
	}
	// Even when a callback is present, a non-owner may not regain local RW.
	s.RealmID = "guest"
	s.pollOnce(ctx, wc, headPath)
	checkRW(doc, false)
	s.RealmID = "owner"
	s.pollOnce(ctx, wc, headPath)
	checkRW(doc, true)
	if lock, err := cli.LockInfo(ctx, wc, doc); err != nil || lock != nil {
		t.Fatalf("poll reacquired released lock: %+v, %v", lock, err)
	}
	checkRW(private, false)
	checkRW(filepath.Join(wc, "append-only.txt"), false)
}
