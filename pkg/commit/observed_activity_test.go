package commit

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/activity"
	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"filees/pkg/watcher"
)

func TestIncomingUpdateNeverEntersOutgoingQueueRealSVN(t *testing.T) {
	for _, bin := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s unavailable", bin)
		}
	}
	run := func(bin string, args ...string) string {
		t.Helper()
		out, err := exec.Command(bin, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", bin, args, err, out)
		}
		return string(out)
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	a := filepath.Join(root, "author")
	b := filepath.Join(root, "reader")
	p := filepath.ToSlash(repo)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	repoURL := (&url.URL{Scheme: "file", Path: p}).String()
	run("svnadmin", "create", repo)
	run("svn", "checkout", repoURL, a)
	run("svn", "checkout", repoURL, b)
	write := func(wc, rel, body string) {
		t.Helper()
		path := filepath.Join(wc, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(a, "01_WYDANIE/WYCENA/01_EDITABLES/przed.ath", "remote before")
	write(a, "01_WYDANIE/WYCENA/inw.pdf", "remote pdf")
	write(a, "mobile-uploads/Kiwerska/remote.txt", "remote move")
	run("svn", "add", filepath.Join(a, "01_WYDANIE"), filepath.Join(a, "mobile-uploads"))
	run("svn", "commit", "-m", "remote upload and move", a)
	journal, err := activity.Open(filepath.Join(root, "activity.json"), 100)
	if err != nil {
		t.Fatal(err)
	}
	cli := client.New(client.Options{Timeout: 10 * time.Second})
	s := &Service{Cli: cli, RepoURL: repoURL, wc: b, repoID: "repo", Activity: journal, staging: make(map[string]*stageItem), cachePath: filepath.Join(b, ".filees", "commit_cache", "cache.json")}
	var sawPending bool
	s.Emit = func(kind string, _ any) {
		if kind == contract.EvActivityChanged {
			for _, e := range journal.List() {
				if e.Stage == activity.Pending || e.Stage == activity.Publishing || e.Stage == activity.Published {
					sawPending = true
				}
			}
		}
	}
	out, err := cli.Update(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	s.RecordUpdate(t.Context(), "repo", b, out)
	var events []watcher.Event
	for rel, op := range updateActivityPaths(out) {
		typ := watcher.EntryFile
		if info, _ := os.Stat(filepath.Join(b, rel)); info != nil && info.IsDir() {
			typ = watcher.EntryDir
		}
		events = append(events, watcher.Event{Rel: rel, Path: filepath.Join(b, rel), Op: op, Type: typ})
	}
	if len(events) != 8 {
		t.Fatalf("notifications=%d: %q", len(events), out)
	}
	s.acceptEvents(t.Context(), events)
	if sawPending || s.stagingLen() != 0 {
		t.Fatalf("incoming queue=%d pending event=%v", s.stagingLen(), sawPending)
	}
	for _, e := range journal.List() {
		if e.Stage != activity.Received || e.Revision != 1 {
			t.Fatalf("incoming receipt=%+v", e)
		}
	}
	// Replay after a daemon restart with the durable journal, but no memory receipts.
	reopened, err := activity.Open(filepath.Join(root, "activity.json"), 100)
	if err != nil {
		t.Fatal(err)
	}
	s.Activity = reopened
	s.acceptEvents(t.Context(), events)
	if s.stagingLen() != 0 {
		t.Fatal("replayed receive entered queue")
	}
	// A remote move must not be restaged even if the watcher detects a rename.
	run("svn", "move", filepath.Join(a, "mobile-uploads/Kiwerska/remote.txt"), filepath.Join(a, "01_WYDANIE/WYCENA/moved.txt"))
	run("svn", "commit", "-m", "remote move", a)
	out, err = cli.Update(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	s.RecordUpdate(t.Context(), "repo", b, out)
	s.acceptEvent(watcher.Event{Rel: "01_WYDANIE/WYCENA/moved.txt", Path: filepath.Join(b, "01_WYDANIE/WYCENA/moved.txt"), OldRel: "mobile-uploads/Kiwerska/remote.txt", Type: watcher.EntryFile, Op: watcher.Renamed})
	if s.stagingLen() != 0 {
		t.Fatal("remote move entered outgoing queue")
	}
	// Download then local edit before watcher processing: only the local edit queues.
	write(a, "01_WYDANIE/WYCENA/inw.pdf", "remote v3")
	run("svn", "commit", "-m", "remote edit", a)
	out, err = cli.Update(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	s.RecordUpdate(t.Context(), "repo", b, out)
	write(b, "01_WYDANIE/WYCENA/inw.pdf", "concurrent local edit")
	s.acceptEvent(watcher.Event{Rel: "01_WYDANIE/WYCENA/inw.pdf", Path: filepath.Join(b, "01_WYDANIE/WYCENA/inw.pdf"), Op: watcher.Modified, Type: watcher.EntryFile})
	if s.stagingLen() != 1 {
		t.Fatal("local edit swallowed")
	}
	s.wcOpMu.Lock()
	s.reconcileCleanPending(context.Background(), b)
	s.wcOpMu.Unlock()
	if s.stagingLen() != 1 {
		t.Fatal("local edit lost on reconcile")
	}
	// A later receipt must not overwrite the presentation of queued local work.
	s.RecordUpdate(t.Context(), "repo", b, "U    01_WYDANIE/WYCENA/inw.pdf\n")
	for _, entry := range reopened.List() {
		if entry.Path == "01_WYDANIE/WYCENA/inw.pdf" && entry.Stage != activity.Pending {
			t.Fatalf("incoming receipt hid local work: %+v", entry)
		}
	}
	// Added may be a delayed download notification followed by a local edit.
	s.acceptEvent(watcher.Event{Rel: "01_WYDANIE/WYCENA/inw.pdf", Path: filepath.Join(b, "01_WYDANIE/WYCENA/inw.pdf"), Op: watcher.Added, Type: watcher.EntryFile})
	s.Rules = Rules{MaxBatchFiles: 20, MaxBatchBytes: 1024 * 1024}
	var authorized bool
	s.BeginPublish = func(_ context.Context, paths []string) (func(), error) {
		authorized = len(paths) == 1 && paths[0] == filepath.Join(b, "01_WYDANIE/WYCENA/inw.pdf")
		return func() {}, nil
	}
	if err := s.tryCommitMode(t.Context(), b, true); err != nil {
		t.Fatal(err)
	}
	if s.stagingLen() != 0 {
		t.Fatal("local edit following downloaded addition cannot publish")
	}
	if !authorized {
		t.Fatal("downloaded addition followed by an edit bypassed the publication guard")
	}
	if rev, err := cli.Revision(t.Context(), repoURL); err != nil || rev != 4 {
		t.Fatalf("local edit revision=%d err=%v, want 4", rev, err)
	}
}

func TestUpdateNotificationsRejectAmbiguousOrEscapingPaths(t *testing.T) {
	got := updateActivityPaths("A    good name.txt\r\nU    dir/file\nD    old\nG    merged\nC    conflict\n   C tree\nA    ../escape\nA    /absolute\nA    C:\\escape\nSkipped 'lost'\n")
	if len(got) != 3 || got["old"] != watcher.Deleted {
		t.Fatalf("notifications=%v", got)
	}
}
