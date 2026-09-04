package shout

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"filees/pkg/client"
)

// TestSVNTwoWorkingCopiesExchangeAndAcknowledgeAnnouncement is the mechanical
// two-desktop gate. It uses a real repository and two independent working
// copies: the author stores the marker only in svn:log, while the receiver
// discovers it only after the corresponding update reaches its disk.
func TestSVNTwoWorkingCopiesExchangeAndAcknowledgeAnnouncement(t *testing.T) {
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn is not installed")
	}
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is not installed")
	}

	root := t.TempDir()
	if runtime.GOOS == "windows" {
		// The sandboxed Windows test process can create its standard temporary
		// directory, but the separately executed TortoiseSVN binary cannot query
		// its canonical case there (E720005). Keep the real-SVN fixture under the
		// writable package directory instead.
		root, err = os.MkdirTemp(".", ".shout-svn-*")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
		root, err = filepath.Abs(root)
		if err != nil {
			t.Fatal(err)
		}
	}
	repository := filepath.Join(root, "repository")
	runShoutCommand(t, "", svnadmin, "create", repository)
	repositoryURLPath := filepath.ToSlash(repository)
	if runtime.GOOS == "windows" {
		repositoryURLPath = "/" + repositoryURLPath
	}
	repoURL := (&url.URL{Scheme: "file", Path: repositoryURLPath}).String()

	authorWC := filepath.Join(root, "author")
	receiverWC := filepath.Join(root, "receiver")
	cli := client.New(client.Options{SvnPath: svn})
	ctx := context.Background()
	if _, err := cli.Checkout(ctx, repoURL, authorWC); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Checkout(ctx, repoURL, receiverWC); err != nil {
		t.Fatal(err)
	}

	document := filepath.Join(authorWC, "document.txt")
	if err := os.WriteFile(document, []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Add(ctx, authorWC, []string{document}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Commit(ctx, authorWC, []string{document}, "seed"); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Update(ctx, receiverWC); err != nil {
		t.Fatal(err)
	}

	logger, ok := cli.(interface {
		LogMessages(context.Context, string, int64, int64) ([]client.LogMessage, error)
	})
	if !ok {
		t.Fatal("real SVN client does not expose log discovery")
	}
	fetch := func(from, to int64) ([]LogEntry, error) {
		entries, err := logger.LogMessages(ctx, receiverWC, from, to)
		if err != nil {
			return nil, err
		}
		result := make([]LogEntry, 0, len(entries))
		for _, entry := range entries {
			result = append(result, LogEntry{Revision: entry.Revision, Message: entry.Message})
		}
		return result, nil
	}
	receiverRevision, err := cli.Revision(ctx, receiverWC)
	if err != nil {
		t.Fatal(err)
	}
	if added, err := Advance(receiverWC, "docs", receiverRevision, fetch, time.Now()); err != nil || len(added) != 0 {
		t.Fatalf("receiver baseline added=%#v err=%v", added, err)
	}

	if err := os.WriteFile(document, []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Commit(ctx, authorWC, []string{document}, Format("Materiały do przeglądu")); err != nil {
		t.Fatal(err)
	}
	// The marker already exists remotely, but no local update means no notice.
	if added, err := Advance(receiverWC, "docs", receiverRevision, fetch, time.Now()); err != nil || len(added) != 0 {
		t.Fatalf("notice arrived before update: added=%#v err=%v", added, err)
	}

	if _, err := cli.Update(ctx, receiverWC); err != nil {
		t.Fatal(err)
	}
	receiverRevision, err = cli.Revision(ctx, receiverWC)
	if err != nil {
		t.Fatal(err)
	}
	added, err := Advance(receiverWC, "docs", receiverRevision, fetch, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0].Revision != receiverRevision || added[0].Title != "Materiały do przeglądu" || added[0].Acked {
		t.Fatalf("received announcement = %#v", added)
	}
	if err := Ack(receiverWC, added[0].ID); err != nil {
		t.Fatal(err)
	}
	recent, err := RecentNotices(receiverWC, 20)
	if err != nil || len(recent) != 1 || !recent[0].Acked {
		t.Fatalf("acknowledged inbox = %#v err=%v", recent, err)
	}

	// A client first installed at current HEAD establishes its baseline and
	// must not replay historical announcements.
	newWC := filepath.Join(root, "new-installation")
	if _, err := cli.Checkout(ctx, repoURL, newWC); err != nil {
		t.Fatal(err)
	}
	newRevision, err := cli.Revision(ctx, newWC)
	if err != nil {
		t.Fatal(err)
	}
	if added, err := Advance(newWC, "docs", newRevision, fetch, time.Now()); err != nil || len(added) != 0 {
		t.Fatalf("new installation replayed history: added=%#v err=%v", added, err)
	}
}

func runShoutCommand(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
}
