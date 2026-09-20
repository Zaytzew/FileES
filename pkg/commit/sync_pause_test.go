package commit

import (
	"filees/pkg/runtime"
	"filees/pkg/watcher"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDraftPreservesEveryRepoQueueUntilExplicitShout(t *testing.T) {
	gate := &runtime.SyncPause{}
	makeRepo := func(id string) (*Service, *stagingClient, string) {
		wc := t.TempDir()
		abs := filepath.Join(wc, "a.txt")
		if err := os.WriteFile(abs, []byte(id), 0600); err != nil {
			t.Fatal(err)
		}
		cli := &stagingClient{statuses: map[string]string{"a.txt": "unversioned"}, commitOut: "Committed revision 22.\n", revision: 22}
		s := &Service{Pause: gate, Cli: cli, repoID: id, Rules: Rules{MaxBatchFiles: 10}, staging: map[string]*stageItem{"a.txt": {Rel: "a.txt", Abs: abs, Op: watcher.Added, FirstSeen: time.Now().Add(-time.Hour), LastSeen: time.Now().Add(-time.Hour)}}}
		return s, cli, wc
	}
	a, ca, wa := makeRepo("a")
	b, cb, wb := makeRepo("b")
	if ready, err := gate.Draft("draft", "a", "begin"); err != nil || !ready {
		t.Fatal(ready, err)
	}
	for _, pair := range []struct {
		s  *Service
		wc string
	}{{a, wa}, {b, wb}} {
		if err := pair.s.tryCommit(t.Context(), pair.wc); err != nil {
			t.Fatal(err)
		}
		if pair.s.stagingLen() != 1 {
			t.Fatal("pause consumed queue")
		}
	}
	if ca.commits != 0 || cb.commits != 0 {
		t.Fatal("automatic publication escaped pause")
	}
	rev, err := a.RequestPublish(runtime.WithDraft(t.Context(), "draft"), wa, "ready now")
	if err != nil || rev != 22 || !strings.Contains(ca.commitMessage, "ready now") {
		t.Fatal(rev, err, ca.commitMessage)
	}
	gate.SetManual(true)
	_, _ = gate.Draft("draft", "a", "end")
	if err := b.tryCommit(t.Context(), wb); err != nil {
		t.Fatal(err)
	}
	if cb.commits != 0 || b.stagingLen() != 1 {
		t.Fatal("editor end resumed manual pause")
	}
	gate.SetManual(false)
	if err := b.tryCommit(t.Context(), wb); err != nil {
		t.Fatal(err)
	}
	if cb.commits != 1 || b.stagingLen() != 0 {
		t.Fatal("resume did not publish preserved queue")
	}
}
func TestManualPauseSkipsPollWithoutTouchingClient(t *testing.T) {
	gate := &runtime.SyncPause{}
	gate.SetManual(true)
	(&Service{Pause: gate}).pollOnce(t.Context(), t.TempDir(), "") // nil client would panic if entered
}
