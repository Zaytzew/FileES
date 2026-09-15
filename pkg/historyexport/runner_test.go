package historyexport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/pkg/client"
)

const fakeRoot = "svn://office/projekt"

// fakeTree answers like the native helper over an in-memory repository.
type fakeTree struct {
	mu      sync.Mutex
	files   map[string]string
	dirs    map[string]bool
	special map[string]bool
	listed  []string
	fetches int
	block   chan struct{}
	started chan struct{}
}

func newFakeTree() *fakeTree {
	return &fakeTree{
		files: map[string]string{
			"Docs/a.txt": "aaa", "Docs/b.txt": "bb", "a.txt": "x", "A.txt": "yy", "CON.txt": "c",
			".filees-whales/g/data": "big", "link": "link a.txt",
		},
		dirs:    map[string]bool{"Docs": true, "empty": true, ".filees-whales": true, ".filees-whales/g": true},
		special: map[string]bool{"link": true},
	}
}

func (f *fakeTree) HistoryListTree(_ context.Context, dirURL string, revision int64, planFile string) (client.HistoryTreeSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listed = append(f.listed, dirURL)
	prefix, err := url.PathUnescape(strings.TrimPrefix(strings.TrimPrefix(dirURL, fakeRoot), "/"))
	if err != nil || (prefix != "" && !f.dirs[prefix]) {
		return client.HistoryTreeSummary{}, errors.New("not a directory")
	}
	under := func(p string) (string, bool) {
		if prefix == "" {
			return p, true
		}
		return strings.CutPrefix(p, prefix+"/")
	}
	summary := client.HistoryTreeSummary{Revision: revision}
	var lines []string
	for p := range f.dirs {
		if rel, ok := under(p); ok {
			line, _ := json.Marshal(map[string]any{"path": rel, "kind": "dir"})
			lines = append(lines, string(line))
			summary.Dirs++
		}
	}
	for p, body := range f.files {
		if rel, ok := under(p); ok {
			line, _ := json.Marshal(map[string]any{"path": rel, "kind": "file", "size": len(body)})
			lines = append(lines, string(line))
			summary.Files++
			summary.Bytes += int64(len(body))
		}
	}
	sort.Strings(lines)
	return summary, os.WriteFile(planFile, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

func (f *fakeTree) HistoryFetchTree(ctx context.Context, rootURL string, revision int64, dest string, pairs []client.HistoryTreePair) (client.HistoryTreeReceipt, error) {
	f.mu.Lock()
	f.fetches++
	block, started := f.block, f.started
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return client.HistoryTreeReceipt{}, ctx.Err()
		}
	}
	var receipt client.HistoryTreeReceipt
	for _, pair := range pairs {
		if f.special[pair.RepoPath] {
			receipt.Skipped = append(receipt.Skipped, client.HistoryTreeSkip{LocalPath: pair.LocalPath, Reason: "special"})
			continue
		}
		body, ok := f.files[pair.RepoPath]
		if !ok {
			return receipt, fmt.Errorf("%s not in revision", pair.RepoPath)
		}
		file, err := os.OpenFile(filepath.Join(dest, filepath.FromSlash(pair.LocalPath)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return receipt, err
		}
		_, err = file.WriteString(body)
		file.Close()
		if err != nil {
			return receipt, err
		}
		receipt.Files = append(receipt.Files, client.HistoryTreeFile{LocalPath: pair.LocalPath, Bytes: int64(len(body))})
	}
	return receipt, nil
}

var exportMoment = time.Date(2026, 9, 12, 10, 0, 0, 0, time.FixedZone("CEST", 2*3600))

const exportFolder = "Projekt_2026-09-12_10-00-00_r7"

func opID(n int) string { return fmt.Sprintf("%032x", n) }

func newRunner(t *testing.T, tree *fakeTree) *Runner {
	return &Runner{
		Journal:    t.TempDir(),
		Reader:     func(string) (Reader, error) { return tree, nil },
		FoldCase:   true,
		Available:  func(string) (int64, error) { return 1 << 40, nil },
		BatchFiles: 2,
	}
}

func exportRequest(n int, parent string) Request {
	return Request{ID: opID(n), ServerID: "office", RepoID: "repo-1", RepoName: "Projekt", RepoURL: fakeRoot,
		RepositoryUUID: "uuid-1", Revision: 7, Moment: exportMoment, Parent: parent}
}

func waitState(t *testing.T, r *Runner, id, want string) Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, err := r.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if rec.State == want {
			return rec
		}
		switch rec.State {
		case StateComplete, StateFailed, StateCancelled, StateInterrupted:
			t.Fatalf("state %s (error %q), want %s", rec.State, rec.Error, want)
		}
		if time.Now().After(deadline) {
			t.Fatalf("still %s, want %s", rec.State, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range list {
		names = append(names, entry.Name())
	}
	return names
}

func TestExportWholeStateLandsInANewDatedFolder(t *testing.T) {
	tree := newFakeTree()
	r := newRunner(t, tree)
	parent := t.TempDir()
	rec, err := r.Begin(exportRequest(1, parent))
	if err != nil || rec.State != StatePlanning {
		t.Fatalf("begin: %+v %v", rec, err)
	}
	planned := waitState(t, r, rec.ID, StatePlanned)
	if planned.FilesTotal != 5 || planned.BytesTotal != int64(len("aaa")+len("bb")+len("x")+len("yy")+len("link a.txt")) {
		t.Fatalf("totals: %d files, %d bytes", planned.FilesTotal, planned.BytesTotal)
	}
	if len(planned.Skipped) != 1 || planned.Skipped[0].RepoPath != "CON.txt" || !planned.WhaleExcluded || planned.WhaleBytes != 3 {
		t.Fatalf("plan shown for confirmation: %+v", planned)
	}
	if len(planned.Renamed) != 1 || planned.Renamed[0] != (Rename{RepoPath: "A.txt", LocalPath: "a(A).txt"}) {
		t.Fatalf("renamed = %+v", planned.Renamed)
	}
	if _, err := r.Confirm(rec.ID); err != nil {
		t.Fatal(err)
	}
	done := waitState(t, r, rec.ID, StateComplete)

	final := filepath.Join(parent, exportFolder)
	if done.Final != final || done.FilesDone != 5 || done.BytesDone != 8 {
		t.Fatalf("done = %+v", done)
	}
	if readFile(t, filepath.Join(final, "Docs", "a.txt")) != "aaa" || readFile(t, filepath.Join(final, "a(A).txt")) != "yy" || readFile(t, filepath.Join(final, "a.txt")) != "x" {
		t.Fatal("exported content differs")
	}
	if info, err := os.Stat(filepath.Join(final, "empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty folder lost: %v", err)
	}
	for _, missing := range []string{"link", "CON.txt", ".filees-whales"} {
		if _, err := os.Lstat(filepath.Join(final, missing)); !os.IsNotExist(err) {
			t.Fatalf("%s exported (err=%v)", missing, err)
		}
	}
	var report exportReport
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(final, ReportName))), &report); err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, skip := range report.Skipped {
		reasons[skip.RepoPath] = skip.Reason
	}
	if report.Revision != 7 || report.Files != 4 || !report.WhaleExcluded || reasons["link"] != ReasonSpecial || reasons["CON.txt"] != "reserved_device" {
		t.Fatalf("report = %+v", report)
	}
	if names := entries(t, parent); len(names) != 1 || names[0] != exportFolder {
		t.Fatalf("parent holds %v; staging must be gone", names)
	}
	if tree.fetches != 3 {
		t.Fatalf("fetch-tree calls = %d, want 3 batches of at most 2", tree.fetches)
	}
}

func TestExportNeverTakesAnExistingFolder(t *testing.T) {
	r := newRunner(t, newFakeTree())
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, exportFolder), 0700); err != nil {
		t.Fatal(err)
	}
	rec, err := r.Begin(exportRequest(2, parent))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, r, rec.ID, StatePlanned)
	if _, err := r.Confirm(rec.ID); err != nil {
		t.Fatal(err)
	}
	done := waitState(t, r, rec.ID, StateComplete)
	if done.Final != filepath.Join(parent, exportFolder+"_2") {
		t.Fatalf("final = %s", done.Final)
	}
	if names := entries(t, filepath.Join(parent, exportFolder)); len(names) != 0 {
		t.Fatalf("existing empty folder was adopted: %v", names)
	}
}

func TestExportRefusesADestinationInsideAWorkingCopy(t *testing.T) {
	r := newRunner(t, newFakeTree())
	managed := t.TempDir()
	r.Roots = func() []string { return []string{managed} }
	if _, err := r.Begin(exportRequest(3, managed)); !errors.Is(err, ErrDestination) {
		t.Fatalf("managed root accepted: %v", err)
	}
	unmanaged := t.TempDir()
	if err := os.MkdirAll(filepath.Join(unmanaged, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(unmanaged, "deep", "er")
	if err := os.MkdirAll(inner, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Begin(exportRequest(4, inner)); !errors.Is(err, ErrDestination) {
		t.Fatalf("folder inside an unmanaged working copy accepted: %v", err)
	}
	if _, err := r.Begin(exportRequest(5, filepath.Join(t.TempDir(), "missing"))); !errors.Is(err, ErrDestination) {
		t.Fatalf("missing parent accepted: %v", err)
	}
	if _, err := r.Begin(exportRequest(6, "relative")); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("relative parent accepted: %v", err)
	}
	if names := entries(t, managed); len(names) != 0 {
		t.Fatalf("refused export created %v", names)
	}
	if names := entries(t, inner); len(names) != 0 {
		t.Fatalf("refused export created %v", names)
	}
}

func TestExportChecksFreeSpaceBeforeTheTransfer(t *testing.T) {
	r := newRunner(t, newFakeTree())
	r.Available = func(string) (int64, error) { return 100, nil }
	parent := t.TempDir()
	rec, err := r.Begin(exportRequest(7, parent))
	if err != nil {
		t.Fatal(err)
	}
	if planned := waitState(t, r, rec.ID, StatePlanned); planned.SpaceAvailable != 100 {
		t.Fatalf("space shown = %d", planned.SpaceAvailable)
	}
	if _, err := r.Confirm(rec.ID); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("confirm without space: %v", err)
	}
	if still, _ := r.Get(rec.ID); still.State != StatePlanned {
		t.Fatalf("state after refusal = %s", still.State)
	}
	cancelled, err := r.Cancel(rec.ID)
	if err != nil || cancelled.State != StateCancelled {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}
	if names := entries(t, parent); len(names) != 0 {
		t.Fatalf("cancelled export left %v", names)
	}
}

func TestExportCancelDuringTransferRemovesOnlyItsOwnStaging(t *testing.T) {
	tree := newFakeTree()
	tree.block, tree.started = make(chan struct{}), make(chan struct{}, 1)
	r := newRunner(t, tree)
	parent := t.TempDir()
	keep := filepath.Join(parent, "keep.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0600); err != nil {
		t.Fatal(err)
	}
	rec, err := r.Begin(exportRequest(8, parent))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, r, rec.ID, StatePlanned)
	if _, err := r.Confirm(rec.ID); err != nil {
		t.Fatal(err)
	}
	<-tree.started
	cancelled, err := r.Cancel(rec.ID)
	if err != nil || cancelled.State != StateCancelled || cancelled.Error != "" {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}
	if names := entries(t, parent); len(names) != 1 || names[0] != "keep.txt" || readFile(t, keep) != "mine" {
		t.Fatalf("parent after cancel = %v", names)
	}
	if again, err := r.Cancel(rec.ID); err != nil || again.State != StateCancelled {
		t.Fatalf("second cancel: %+v %v", again, err)
	}
}

func TestExportRecoversAfterARestart(t *testing.T) {
	t.Run("unfinished transfer is interrupted", func(t *testing.T) {
		tree := newFakeTree()
		tree.block, tree.started = make(chan struct{}), make(chan struct{}, 1)
		before := newRunner(t, tree)
		parent := t.TempDir()
		rec, err := before.Begin(exportRequest(9, parent))
		if err != nil {
			t.Fatal(err)
		}
		waitState(t, before, rec.ID, StatePlanned)
		if _, err := before.Confirm(rec.ID); err != nil {
			t.Fatal(err)
		}
		<-tree.started
		after := &Runner{Journal: before.Journal, Reader: before.Reader}
		if err := after.Recover(); err != nil {
			t.Fatal(err)
		}
		got, err := after.Get(rec.ID)
		if err != nil || got.State != StateInterrupted || got.Error == "" {
			t.Fatalf("after restart: %+v %v", got, err)
		}
		if names := entries(t, parent); len(names) != 0 {
			t.Fatalf("interrupted export left %v", names)
		}
		_, _ = before.Cancel(rec.ID)
	})
	t.Run("half-moved result is completed", func(t *testing.T) {
		r := newRunner(t, newFakeTree())
		parent := t.TempDir()
		id := opID(10)
		stage := filepath.Join(parent, stagePrefix+id)
		final := filepath.Join(parent, exportFolder)
		for _, dir := range []string{filepath.Join(stage, "tree", "Docs"), final} {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(stage, "tree", "Docs", "a.txt"), []byte("aaa"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(final, "moved.txt"), []byte("already"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := r.save(Record{Request: exportRequest(10, parent), State: StateFinalizing, Stage: stage, Final: final}); err != nil {
			t.Fatal(err)
		}
		if err := r.Recover(); err != nil {
			t.Fatal(err)
		}
		got, _ := r.Get(id)
		if got.State != StateComplete || readFile(t, filepath.Join(final, "Docs", "a.txt")) != "aaa" || readFile(t, filepath.Join(final, "moved.txt")) != "already" {
			t.Fatalf("recovered = %+v", got)
		}
		if names := entries(t, parent); len(names) != 1 {
			t.Fatalf("staging not removed: %v", names)
		}
	})
}

func TestExportSelectedFilesAndOneFolder(t *testing.T) {
	tree := newFakeTree()
	r := newRunner(t, tree)
	parent := t.TempDir()
	req := exportRequest(11, parent)
	req.Files = []Node{{Path: "Docs/a.txt", Kind: "file", Size: 3}}
	rec, err := r.Begin(req)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, r, rec.ID, StatePlanned)
	if _, err := r.Confirm(rec.ID); err != nil {
		t.Fatal(err)
	}
	done := waitState(t, r, rec.ID, StateComplete)
	if names := entries(t, done.Final); strings.Join(names, ",") != "Docs,"+ReportName || readFile(t, filepath.Join(done.Final, "Docs", "a.txt")) != "aaa" {
		t.Fatalf("selected export holds %v", names)
	}
	if len(tree.listed) != 0 {
		t.Fatalf("a selection listed the tree: %v", tree.listed)
	}

	req = exportRequest(12, parent)
	req.Subtree = "Docs"
	rec, err = r.Begin(req)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, r, rec.ID, StatePlanned)
	if _, err := r.Confirm(rec.ID); err != nil {
		t.Fatal(err)
	}
	done = waitState(t, r, rec.ID, StateComplete)
	if names := entries(t, filepath.Join(done.Final, "Docs")); strings.Join(names, ",") != "a.txt,b.txt" {
		t.Fatalf("folder export holds %v", names)
	}
	if len(tree.listed) != 1 || tree.listed[0] != fakeRoot+"/Docs" {
		t.Fatalf("listed = %v", tree.listed)
	}
}

func TestExportRefusesBadRequestsAndWrongStates(t *testing.T) {
	r := newRunner(t, newFakeTree())
	parent := t.TempDir()
	for name, mutate := range map[string]func(*Request){
		"bad id":           func(q *Request) { q.ID = "nope" },
		"zero revision":    func(q *Request) { q.Revision = 0 },
		"no moment":        func(q *Request) { q.Moment = time.Time{} },
		"local url":        func(q *Request) { q.RepoURL = "C:/repo" },
		"climbing subtree": func(q *Request) { q.Subtree = "../x" },
		"folder selected":  func(q *Request) { q.Files = []Node{{Path: "Docs", Kind: "dir"}} },
	} {
		req := exportRequest(20, parent)
		mutate(&req)
		if _, err := r.Begin(req); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}
	rec, err := r.Begin(exportRequest(21, parent))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Begin(exportRequest(21, parent)); !errors.Is(err, ErrState) {
		t.Fatalf("duplicate id: %v", err)
	}
	waitState(t, r, rec.ID, StatePlanned)
	if _, err := r.Confirm(rec.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, r, rec.ID, StateComplete)
	if _, err := r.Confirm(rec.ID); !errors.Is(err, ErrState) {
		t.Fatalf("confirm after completion: %v", err)
	}
	if _, err := r.Get(opID(99)); !errors.Is(err, ErrUnknownOperation) {
		t.Fatalf("unknown operation: %v", err)
	}
}
