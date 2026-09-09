package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/activity"
	"filees/pkg/shout"
	"filees/pkg/watcher"
)

type transactionFake struct {
	*stagingClient
	wc                   string
	mutations, lookups   int
	failLookup, noEffect bool
	before               func()
}

func (c *transactionFake) CommitHead(context.Context, string) (int64, error) { return 4, nil }
func (c *transactionFake) CommitWithID(_ context.Context, _, _ string, _ []string, _ string, _ bool, _ string, _ int64) (string, int64, error) {
	c.mutations++
	if c.before != nil {
		c.before()
	}
	if !HasUnresolvedCommit(c.wc) {
		panic("mutation started without durable intent")
	}
	if !c.noEffect {
		c.statuses["a.txt"] = "normal"
	}
	return "", 0, errors.New("reply lost")
}
func (c *transactionFake) FindCommit(context.Context, string, string, int64) (int64, error) {
	c.lookups++
	if c.failLookup || c.noEffect {
		return 0, errors.New("lookup cannot prove effect")
	}
	return 5, nil
}

type failingPublication struct {
	*activity.Journal
	fail bool
}

func (j *failingPublication) Record(e activity.Entry) error {
	if j.fail && e.Stage == activity.Published {
		return errors.New("journal disk write failed")
	}
	return j.Journal.Record(e)
}

func transactionFixture(t *testing.T) (*Service, *transactionFake, *failingPublication, string) {
	t.Helper()
	wc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wc, ".filees", "state"), 0700); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(wc, "a.txt")
	if err := os.WriteFile(abs, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &transactionFake{stagingClient: &stagingClient{statuses: map[string]string{"a.txt": "modified"}}, wc: wc}
	j, err := activity.Open(filepath.Join(t.TempDir(), "journal.json"), 20)
	if err != nil {
		t.Fatal(err)
	}
	fj := &failingPublication{Journal: j}
	s := &Service{Cli: c, RepoURL: "file:///synthetic", repoID: "recovery", wc: wc, Rules: Rules{NewLatency: time.Nanosecond, MaxBatchFiles: 10}, Activity: fj, cachePath: filepath.Join(wc, ".filees", "commit_cache", "cache.json"), staging: make(map[string]*stageItem)}
	s.acceptEvent(watcher.Event{Path: abs, Rel: "a.txt", Type: watcher.EntryFile, Op: watcher.Modified})
	return s, c, fj, wc
}

func TestDurableCommitProjectionWriteFailureRetainsReceipt(t *testing.T) {
	s, c, j, wc := transactionFixture(t)
	j.fail = true
	if _, err := s.RequestPublish(t.Context(), wc, "once"); err == nil {
		t.Fatal("journal failure hidden")
	}
	in, err := s.readIntent(wc)
	if err != nil || in.Phase != "confirmed" || in.Revision != 5 {
		t.Fatal(in, err)
	}
	if len(s.staging) != 1 {
		t.Fatal("queue lost before journal persisted")
	}
	// Make the remote endpoint unavailable: persisted receipt must suffice.
	c.failLookup = true
	j.fail = false
	if _, err := s.recoverCommit(t.Context(), wc); err != nil {
		t.Fatal(err)
	}
	if c.mutations != 1 || c.lookups != 1 || len(j.List()) != 1 || j.List()[0].Revision != 5 {
		t.Fatal("receipt not replayed idempotently", c.mutations, c.lookups, j.List())
	}
	if HasUnresolvedCommit(wc) {
		t.Fatal("receipt not acknowledged")
	}
	if _, err := s.recoverCommit(t.Context(), wc); err != nil {
		t.Fatal(err)
	}
	if c.mutations != 1 || c.lookups != 1 {
		t.Fatal("done record replayed")
	}
}

func TestDurableCommitUnknownResultBlocksRetirementAndRetry(t *testing.T) {
	s, c, _, wc := transactionFixture(t)
	c.noEffect = true
	if err := s.tryCommit(t.Context(), wc); err == nil {
		t.Fatal("missing failure")
	}
	in, _ := s.readIntent(wc)
	// Even later clean status is NOT evidence that this intent had no effect.
	c.statuses["a.txt"] = "normal"
	s.reconcileCleanPending(t.Context(), wc)
	s.acceptEvents(t.Context(), []watcher.Event{{Path: filepath.Join(wc, "a.txt"), Rel: "a.txt", Type: watcher.EntryFile, Op: watcher.Modified}})
	if len(s.staging) != 1 {
		t.Fatal("uncertain state discarded")
	}
	if err := s.tryCommit(t.Context(), wc); err == nil {
		t.Fatal("unknown result silently accepted")
	}
	after, _ := s.readIntent(wc)
	if c.mutations != 1 || after.ID != in.ID {
		t.Fatal("unsafe blind retry")
	}
}

func TestDurableCommitRefusesMutationWithoutCache(t *testing.T) {
	s, c, _, wc := transactionFixture(t)
	s.cachePath = ""
	if err := s.tryCommit(t.Context(), wc); err == nil || c.mutations != 0 {
		t.Fatal("mutation without durable cache", err, c.mutations)
	}
}

func TestDurableCommitCorruptIntentFailClosed(t *testing.T) {
	s, c, _, wc := transactionFixture(t)
	if err := os.WriteFile(transactionPath(wc), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	c.statuses["a.txt"] = "normal"
	s.reconcileCleanPending(t.Context(), wc)
	if err := s.tryCommit(t.Context(), wc); err == nil || c.mutations != 0 || len(s.staging) != 1 {
		t.Fatal("invalid state ignored", err)
	}
	if !HasUnresolvedCommit(wc) {
		t.Fatal("startup gate missed corrupt state")
	}
}

func TestDurableCommitDoesNotRegressShoutCursor(t *testing.T) {
	s, _, _, wc := transactionFixture(t)
	if err := shout.SaveLastSeen(wc, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestPublish(t.Context(), wc, "once"); err != nil {
		t.Fatal(err)
	}
	seen, _, err := shout.LoadLastSeen(wc)
	if err != nil || seen != 20 {
		t.Fatal("cursor regressed", seen, err)
	}
}

func TestDurableCommitRecoveryDoesNotConfirmDifferentShout(t *testing.T) {
	s, c, _, wc := transactionFixture(t)
	c.failLookup = true
	if _, err := s.RequestPublish(t.Context(), wc, "first request"); err == nil {
		t.Fatal("missing loss")
	}
	c.failLookup = false
	if _, err := s.RequestPublish(t.Context(), wc, "different request"); err == nil {
		t.Fatal("unrelated request falsely confirmed")
	}
	if c.mutations != 1 {
		t.Fatal("second mutation during recovery")
	}
}

func TestDurableCommitCompletedReceiptAllowsRelocatedWC(t *testing.T) {
	s, _, _, wc := transactionFixture(t)
	if err := s.tryCommit(t.Context(), wc); err != nil {
		t.Fatal(err)
	}
	in, err := s.readIntent(wc)
	if err != nil {
		t.Fatal(err)
	}
	in.WC = filepath.Join(wc, "previous-location")
	if err := s.writeIntent(wc, in); err != nil {
		t.Fatal(err)
	}
	if found, err := s.recoverCommit(t.Context(), wc); found || err != nil {
		t.Fatal("done receipt blocked relocation", found, err)
	}
	in.Phase = "attempting"
	if err := s.writeIntent(wc, in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.recoverCommit(t.Context(), wc); err == nil {
		t.Fatal("unfinished foreign receipt accepted")
	}
}
