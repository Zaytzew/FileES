//go:build native_svn_probe && (windows || linux)

package commit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/svnurl"
	"filees/pkg/client"
	"filees/pkg/watcher"
)

// Owner's desktop, 2026-09-23: new photos organised into a dated subfolder
// before their first commit. Rename detection compares content with the
// previous scan's manifest, so the move reaches the commit as a verified
// rename with no Added before it. Through the native helper the vanished source
// reports status "none" - which the degrade-to-Added branch did not recognise,
// so publication was held and retried every 30 s, logging each time, forever.
//
// The existing Linux-only TestNativeRenameOfUnpublishedFile could not see it:
// it stages Added first, and the event layer folds "added, then moved" into a
// plain add before the rename logic ever runs.
func TestNativeRenameOfANeverPublishedFileIntoANewFolderPublishesItAsNew(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	wc := filepath.Join(root, "wc")
	svn := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("svn", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("svn %v: %v %s", args, err, out)
		}
		return string(out)
	}
	if out, err := exec.Command("svnadmin", "create", repo).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin: %v %s", err, out)
	}
	repoURL := svnurl.File(repo)
	svn(root, "checkout", "--quiet", repoURL, wc)
	for _, p := range []string{".filees/state", ".filees/commit_cache", ".filees/passports"} {
		if err := os.MkdirAll(filepath.Join(wc, p), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	folder := filepath.Join(wc, "01_PROJEKT-WNĘTRZ")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	svn(wc, "add", "--quiet", "01_PROJEKT-WNĘTRZ")
	svn(wc, "commit", "--quiet", "-m", "base")

	name := "HOTE_14_ŁAZIENKA  w pokoju.jpg"
	oldRel := "01_PROJEKT-WNĘTRZ/" + name
	newRel := "01_PROJEKT-WNĘTRZ/23.09.2026/" + name
	if err := os.WriteFile(filepath.Join(wc, filepath.FromSlash(oldRel)), []byte("photo bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(folder, "23.09.2026"), 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(wc, filepath.FromSlash(newRel))
	if err := os.Rename(filepath.Join(wc, filepath.FromSlash(oldRel)), dst); err != nil {
		t.Fatal(err)
	}

	s := &Service{
		Cli: client.New(client.Options{NativeSVNPath: helper, Timeout: 10 * time.Second}), RepoURL: repoURL, wc: wc,
		repoID: "native-fixture", UUID: "native-fixture", Rules: Rules{MaxBatchFiles: 100, MaxBatchBytes: 1024 * 1024},
		staging: map[string]*stageItem{}, cachePath: filepath.Join(wc, ".filees/commit_cache/cache.json"),
	}
	s.addEvent(watcher.Event{Rel: newRel, OldRel: oldRel, Path: dst, Op: watcher.Renamed, Type: watcher.EntryFile, IdentityVerified: true})
	if err := s.tryCommitMode(context.Background(), wc, true); err != nil {
		t.Fatalf("publication held: %v", err)
	}
	if len(s.staging) != 0 {
		t.Fatalf("still pending: %+v", s.staging)
	}
	log := svn(wc, "log", "--xml", "--verbose", "-r", "HEAD", repoURL)
	if strings.Contains(log, "copyfrom-path") {
		t.Fatalf("published as a copy of a path that never existed in the repository:\n%s", log)
	}
	if !strings.Contains(log, "23.09.2026") {
		t.Fatalf("new file was not published:\n%s", log)
	}
	if got := svn(wc, "cat", filepath.ToSlash(newRel)); got != "photo bytes" {
		t.Fatalf("published bytes = %q", got)
	}
}
