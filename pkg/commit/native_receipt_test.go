package commit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/activity"
	contract "filees/pkg/contract/v1"
	"filees/pkg/watcher"
)

type emptyReceiptClient struct {
	*stagingClient
	heads int
}

func (c *emptyReceiptClient) CommitWithRevision(context.Context, string, string, []string, string, bool) (string, int64, error) {
	c.statuses["a.txt"] = "normal"
	return `{"schema":"filees.native-svn/v1","ok":true,"revision":null}`, 0, nil
}
func (c *emptyReceiptClient) Revision(context.Context, string) (int64, error) {
	c.heads++
	return 99, nil
}
func TestNativeNullNeverPublishesForeignHead(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "a.txt")
	if e := os.WriteFile(abs, []byte("work"), 0600); e != nil {
		t.Fatal(e)
	}
	c := &emptyReceiptClient{stagingClient: &stagingClient{statuses: map[string]string{"a.txt": "modified"}}}
	rec := &activityRecorder{}
	published := 0
	s := &Service{Cli: c, Rules: Rules{NewLatency: time.Nanosecond, MaxBatchFiles: 10}, Activity: rec, Emit: func(kind string, _ any) {
		if kind == contract.EvCommitCompleted {
			published++
		}
	}, repoID: "test", staging: make(map[string]*stageItem), cachePath: filepath.Join(wc, ".filees", "commit_cache", "cache.json")}
	s.acceptEvent(watcher.Event{Path: abs, Rel: "a.txt", Type: watcher.EntryFile, Op: watcher.Modified})
	if e := s.tryCommit(context.Background(), wc); e != nil {
		t.Fatal(e)
	}
	if c.heads != 0 || published != 0 || s.shoutUsed {
		t.Fatalf("heads=%d published=%d shout=%v", c.heads, published, s.shoutUsed)
	}
	for _, e := range rec.entries {
		if e.Stage == activity.Published {
			t.Fatal("no-op appeared as publication")
		}
	}
}
func TestNativeConflictReceiptSkipsCLIParser(t *testing.T) {
	p := parseConflicts(`{"schema":"filees.native-svn/v1","ok":true,"revision":2,"conflicts":["dir/新 name.txt"]}`)
	if len(p) != 1 || p[0] != "dir/新 name.txt" {
		t.Fatal(p)
	}
}
