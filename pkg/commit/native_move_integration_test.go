//go:build native_svn_probe && linux

package commit

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/passport"
	"filees/pkg/pathownership"
	"filees/pkg/watcher"
)

type nativeFixture struct {
	wc, repoURL, binary string
	cli                 client.Client
}

func nativeRun(t *testing.T, dir, binary string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %q: %v\n%s", binary, args, err, out)
	}
	return out
}

func newNativeFixture(t *testing.T) nativeFixture {
	t.Helper()
	f := nativeFixture{binary: os.Getenv("FILEES_SVN_PROBE")}
	if f.binary == "" {
		t.Fatal("FILEES_SVN_PROBE required for native integration acceptance")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	f.wc = filepath.Join(root, "wc")
	nativeRun(t, root, "svnadmin", "create", repo)
	f.repoURL = (&url.URL{Scheme: "file", Path: repo}).String()
	nativeRun(t, root, "svn", "checkout", f.repoURL, f.wc)
	for _, p := range []string{".filees/state", ".filees/commit_cache", ".filees/passports"} {
		if err := os.MkdirAll(filepath.Join(f.wc, p), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"old.txt", "duplicate.txt"} {
		if err := os.WriteFile(filepath.Join(f.wc, name), []byte("identical original bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	nativeRun(t, f.wc, "svn", "add", "old.txt", "duplicate.txt")
	nativeRun(t, f.wc, "svn", "propset", "svn:needs-lock", "*", "old.txt")
	nativeRun(t, f.wc, "svn", "commit", "--username", "creator", "-m", "birth")
	nativeRun(t, f.wc, "svn", "update")
	f.cli = client.New(client.Options{NativeSVNPath: f.binary, Timeout: 10 * time.Second})
	return f
}

func (f nativeFixture) service(cli client.Client) *Service {
	return &Service{Cli: cli, RepoURL: f.repoURL, wc: f.wc, repoID: "native-fixture", UUID: "native-fixture", Rules: Rules{NeedsLock: true, MaxBatchFiles: 100, MaxBatchBytes: 1024 * 1024}, staging: map[string]*stageItem{}, cachePath: filepath.Join(f.wc, ".filees/commit_cache/cache.json")}
}

func (f nativeFixture) move(t *testing.T, dst string) watcher.Event {
	t.Helper()
	if err := os.Chmod(filepath.Join(f.wc, "old.txt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(f.wc, dst)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.wc, "old.txt"), filepath.Join(f.wc, dst)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.wc, dst), []byte("edited AFTER move"), 0600); err != nil {
		t.Fatal(err)
	}
	return watcher.Event{Path: filepath.Join(f.wc, dst), Rel: dst, OldRel: "old.txt", Op: watcher.Renamed, Type: watcher.EntryFile, IdentityVerified: true}
}

func (f nativeFixture) verify(t *testing.T, dst string) {
	t.Helper()
	raw := nativeRun(t, f.wc, "svn", "log", "--xml", "--verbose", "--quiet", "-r", "1:HEAD", f.repoURL)
	revs, err := pathownership.ParseLog(raw)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := pathownership.Replay(context.Background(), "native-fixture", int64(len(revs)), revs)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range snap.Entries {
		if e.Path == "old.txt" {
			t.Fatal("source was not deleted in the transaction")
		}
		if e.Path == dst {
			found = true
			if e.FirstCommitter != "creator" || e.CreatedRevision != 1 {
				t.Fatalf("lost creator: %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("destination was not published")
	}
	got, err := os.ReadFile(filepath.Join(f.wc, dst))
	if err != nil || string(got) != "edited AFTER move" {
		t.Fatalf("local data changed: %q %v", got, err)
	}
	if got := string(nativeRun(t, f.wc, "svn", "cat", dst)); got != "edited AFTER move" {
		t.Fatalf("published wrong bytes: %q", got)
	}
}

func TestNativeWatcherThroughCommitWithPassport(t *testing.T) {
	f := newNativeFixture(t)
	manifest := filepath.Join(f.wc, ".filees/state/manifest.json")
	scanner, err := watcher.NewScanner(watcher.Options{WC: f.wc, StatePath: manifest, ScanPeriod: 20 * time.Millisecond, RequireRenameIdentity: true, UseMD5: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := scanner.Start(ctx)
	deadline := time.After(3 * time.Second)
	for {
		if _, err := os.Stat(manifest); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("baseline timeout")
		case <-time.After(5 * time.Millisecond):
		}
	}
	// Actual scanner must recognize the edited move despite duplicate MD5s.
	f.move(t, "new.txt")
	var move watcher.Event
	for move.Op != watcher.Renamed {
		select {
		case move = <-events:
			if move.Op == watcher.RenameUncertain {
				t.Fatal("stable identity not available")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("rename timeout")
		}
	}
	if move.OldRel != "old.txt" || !move.IdentityVerified {
		t.Fatalf("bad rename: %+v", move)
	}
	cancel()
	for range events {
	} // no mutation of fixtures while publishing
	s := f.service(f.cli)
	m, err := passport.Open(filepath.Join(f.wc, ".filees/passports/passports.json"), "test-instance", passport.SVNBackend{Client: f.cli, WC: f.wc}, passport.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s.BeginPublish = func(ctx context.Context, paths []string) (func(), error) {
		if _, _, err := m.Acquire(ctx, paths, "owner"); err != nil {
			return nil, err
		}
		return m.BeginPublish(ctx, paths)
	}
	s.OnPathsRemoved = m.ForgetRemoved
	s.acceptEvent(move)
	s.acceptEvent(watcher.Event{Path: move.Path, Rel: move.Rel, Type: watcher.EntryFile, Op: watcher.Modified})
	if s.staging[move.Rel].Op != watcher.Renamed {
		t.Fatal("edit discarded rename")
	}
	if err := s.tryCommit(context.Background(), f.wc); err != nil {
		t.Fatal(err)
	}
	if len(s.staging) != 0 {
		t.Fatalf("pending after success: %+v", s.staging)
	}
	f.verify(t, "new.txt")
}

func TestNativeRenameDoesNotTrustStaleAdded(t *testing.T) {
	f := newNativeFixture(t)
	s := f.service(f.cli)
	s.addEvent(watcher.Event{Rel: "old.txt", Path: filepath.Join(f.wc, "old.txt"), Op: watcher.Added, Type: watcher.EntryFile})
	s.addEvent(f.move(t, "new.txt"))
	if err := s.tryCommitMode(context.Background(), f.wc, true); err != nil {
		t.Fatal(err)
	}
	f.verify(t, "new.txt")
}

func TestNativeRenameOfUnpublishedFile(t *testing.T) {
	f := newNativeFixture(t)
	s := f.service(f.cli)
	old := filepath.Join(f.wc, "never-published.txt")
	if err := os.WriteFile(old, []byte("new object"), 0600); err != nil {
		t.Fatal(err)
	}
	s.addEvent(watcher.Event{Rel: "never-published.txt", Path: old, Op: watcher.Added, Type: watcher.EntryFile})
	dst := filepath.Join(f.wc, "first-name.txt")
	if err := os.Rename(old, dst); err != nil {
		t.Fatal(err)
	}
	s.addEvent(watcher.Event{Rel: "first-name.txt", OldRel: "never-published.txt", Path: dst, Op: watcher.Renamed, Type: watcher.EntryFile, IdentityVerified: true})
	if err := s.tryCommitMode(context.Background(), f.wc, true); err != nil {
		t.Fatal(err)
	}
	if len(s.staging) != 0 {
		t.Fatalf("pending: %+v", s.staging)
	}
	raw := string(nativeRun(t, f.wc, "svn", "log", "--xml", "--verbose", "-r", "2", f.repoURL))
	if strings.Contains(raw, "copyfrom-path") || strings.Contains(raw, "never-published.txt") || !strings.Contains(raw, "/first-name.txt") {
		t.Fatal(raw)
	}
}

type moveFaultClient struct {
	client.Client
	mover                client.MetadataMover
	failMove, failCommit bool
}

func (c *moveFaultClient) MetadataMovesEnabled() bool { return true }
func (c *moveFaultClient) RecordMove(ctx context.Context, wc, old, dst string) (string, error) {
	state, err := c.mover.RecordMove(ctx, wc, old, dst)
	if err == nil && c.failMove {
		return "", errors.New("injected loss of native reply after actual mutation")
	}
	return state, err
}
func (c *moveFaultClient) VerifyCommittedMove(ctx context.Context, wc, old, dst string) (bool, error) {
	return c.mover.VerifyCommittedMove(ctx, wc, old, dst)
}
func (c *moveFaultClient) CommitKeepLocks(ctx context.Context, wc string, paths []string, msg string) (string, error) {
	out, err := c.Client.CommitKeepLocks(ctx, wc, paths, msg)
	if err == nil && c.failCommit {
		return out, errors.New("injected loss of commit reply after real commit")
	}
	return out, err
}

func TestNativeMoveRestartAfterLostReplies(t *testing.T) {
	for _, phase := range []string{"native", "commit"} {
		t.Run(phase, func(t *testing.T) {
			f := newNativeFixture(t)
			fault := &moveFaultClient{Client: f.cli, mover: f.cli.(client.MetadataMover), failMove: phase == "native", failCommit: phase == "commit"}
			s := f.service(fault)
			s.acceptEvent(f.move(t, "new.txt"))
			if err := s.tryCommit(context.Background(), f.wc); err == nil {
				t.Fatal("missing injected failure")
			}
			// Cache was saved before mutation; discard the whole service instance.
			restarted := f.service(f.cli)
			restarted.loadCache()
			if len(restarted.staging) != 1 {
				t.Fatal("lost durable intent")
			}
			if err := restarted.tryCommit(context.Background(), f.wc); err != nil {
				t.Fatal(err)
			}
			if len(restarted.staging) != 0 {
				t.Fatal("restart did not acknowledge move")
			}
			f.verify(t, "new.txt")
			if rev, err := f.cli.Revision(context.Background(), f.repoURL); err != nil || rev != 2 {
				t.Fatalf("duplicate commit: r%d, %v", rev, err)
			}
		})
	}
}

func TestNativeFailureNeverFallsBack(t *testing.T) {
	for _, fault := range []string{"missing-helper", "source-reappeared", "cache-unavailable", "unverified", "uncertain", "disabled"} {
		t.Run(fault, func(t *testing.T) {
			f := newNativeFixture(t)
			ev := f.move(t, "new.txt")
			s := f.service(f.cli)
			switch fault {
			case "missing-helper":
				s.Cli = client.New(client.Options{NativeSVNPath: filepath.Join(t.TempDir(), "absent")})
			case "source-reappeared":
				if err := os.WriteFile(filepath.Join(f.wc, "old.txt"), []byte("new object in source slot"), 0600); err != nil {
					t.Fatal(err)
				}
			case "cache-unavailable":
				s.cachePath = filepath.Join(f.wc, "absent", "cache.json")
				s.RequireSVNMetadata = true
			case "unverified":
				ev.IdentityVerified = false
			case "uncertain":
				ev.Op = watcher.RenameUncertain
			case "disabled":
				s.Cli = client.New(client.Options{})
			}
			s.acceptEvent(ev)
			before, err := f.cli.Status(context.Background(), f.wc, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.tryCommit(context.Background(), f.wc); err == nil {
				t.Fatal("unsafe move accepted")
			}
			after, err := f.cli.Status(context.Background(), f.wc, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("changed SVN status: before=%v after=%v", before, after)
			}
			if len(s.staging) != 1 {
				t.Fatal("intent was discarded")
			}
			if got, _ := os.ReadFile(ev.Path); string(got) != "edited AFTER move" {
				t.Fatal("lost user edit")
			}
			if rev, _ := f.cli.Revision(context.Background(), f.repoURL); rev != 1 {
				t.Fatal("failure still committed")
			}
		})
	}
}

func TestNativeRenameChainAndNewParent(t *testing.T) {
	f := newNativeFixture(t)
	s := f.service(f.cli)
	s.acceptEvent(f.move(t, "middle.txt"))
	if err := os.Mkdir(filepath.Join(f.wc, "new folder"), 0700); err != nil {
		t.Fatal(err)
	}
	dst := "new folder/Łódź.txt"
	if err := os.Rename(filepath.Join(f.wc, "middle.txt"), filepath.Join(f.wc, dst)); err != nil {
		t.Fatal(err)
	}
	s.acceptEvent(watcher.Event{Path: filepath.Join(f.wc, dst), Rel: dst, OldRel: "middle.txt", Op: watcher.Renamed, Type: watcher.EntryFile, IdentityVerified: true})
	if len(s.staging) != 1 || s.staging[dst].OldRel != "old.txt" {
		t.Fatal("chain did not preserve original source")
	}
	if err := s.tryCommit(context.Background(), f.wc); err != nil {
		t.Fatal(err)
	}
	f.verify(t, dst)
}

func TestNativeScheduledChainBlocksInsteadOfHalfCommit(t *testing.T) {
	f := newNativeFixture(t)
	fault := &moveFaultClient{Client: f.cli, mover: f.cli.(client.MetadataMover), failCommit: false}
	s := f.service(fault)
	ev := f.move(t, "middle.txt")
	s.acceptEvent(ev)
	// Schedule directly and persist the exact phase; then user moves again.
	if _, err := fault.RecordMove(context.Background(), f.wc, "old.txt", "middle.txt"); err != nil {
		t.Fatal(err)
	}
	s.staging["middle.txt"].MoveScheduled = true
	s.saveCache()
	if err := os.Rename(ev.Path, filepath.Join(f.wc, "final.txt")); err != nil {
		t.Fatal(err)
	}
	s.acceptEvent(watcher.Event{Path: filepath.Join(f.wc, "final.txt"), Rel: "final.txt", OldRel: "middle.txt", Op: watcher.Renamed, Type: watcher.EntryFile, IdentityVerified: true})
	err := s.tryCommit(context.Background(), f.wc)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("scheduled chain not held: %v", err)
	}
	if rev, _ := f.cli.Revision(context.Background(), f.repoURL); rev != 1 {
		t.Fatal("half-chain committed")
	}
}

func TestNativeMoveDefersOldDirectoryDeletion(t *testing.T) {
	f := newNativeFixture(t)
	nativeRun(t, f.wc, "svn", "mkdir", "old-dir")
	nativeRun(t, f.wc, "svn", "move", "old.txt", "old-dir/doc.txt")
	nativeRun(t, f.wc, "svn", "commit", "-m", "organize")
	nativeRun(t, f.wc, "svn", "update")
	if err := os.Rename(filepath.Join(f.wc, "old-dir/doc.txt"), filepath.Join(f.wc, "new.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.wc, "old-dir")); err != nil {
		t.Fatal(err)
	}
	s := f.service(f.cli)
	s.acceptEvent(watcher.Event{Path: filepath.Join(f.wc, "old-dir"), Rel: "old-dir", Op: watcher.Deleted, Type: watcher.EntryDir})
	s.acceptEvent(watcher.Event{Path: filepath.Join(f.wc, "new.txt"), Rel: "new.txt", OldRel: "old-dir/doc.txt", Op: watcher.Renamed, Type: watcher.EntryFile, IdentityVerified: true})
	if err := s.tryCommit(context.Background(), f.wc); err != nil {
		t.Fatal(err)
	}
	st, err := f.cli.Status(context.Background(), f.wc, []string{"old-dir"})
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 1 || st[0].Item != "missing" {
		t.Fatalf("old directory deletion staged ahead of child move: %v", st)
	}
	if len(s.staging) != 1 || s.staging["old-dir"] == nil {
		t.Fatal("old parent was not deferred")
	}
	if err := s.tryCommit(context.Background(), f.wc); err != nil {
		t.Fatal(err)
	}
	if len(s.staging) != 0 {
		t.Fatal("empty source directory was not published on next batch")
	}
}
