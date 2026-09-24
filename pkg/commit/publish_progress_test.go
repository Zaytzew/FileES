package commit

import (
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"filees/pkg/watcher"
)

// The totals are what the batch will send: files with content, sized on disk -
// never directories or deletions. Reports are capped at the totals and the
// publication's end is reported as nil.
func TestPublishProgressMeasuresTheBatchAndEnds(t *testing.T) {
	root := t.TempDir()
	write := func(name string, size int) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	pending := []pendingEntry{
		{item: &stageItem{Rel: "a.bin", Abs: write("a.bin", 1000), Op: watcher.Added}},
		{item: &stageItem{Rel: "b.bin", Abs: write("b.bin", 500), Op: watcher.Modified}},
		{item: &stageItem{Rel: "dir", Abs: root, Op: watcher.Added, IsDir: true}},
		{item: &stageItem{Rel: "gone.bin", Abs: filepath.Join(root, "gone.bin"), Op: watcher.Deleted}},
		{item: &stageItem{Rel: "other.bin", Abs: write("other.bin", 9999), Op: watcher.Added}},
	}
	selected := map[string]bool{"a.bin": true, "b.bin": true, "dir": true, "gone.bin": true}
	var reports []*contract.PublishProgress
	service := &Service{OnPublishProgress: func(p *contract.PublishProgress) { reports = append(reports, p) }}

	report, end := service.startPublishProgress(pending, selected)
	if len(reports) != 1 || reports[0].FilesTotal != 2 || reports[0].BytesTotal != 1500 || reports[0].FilesDone != 0 || reports[0].StartedAt == "" {
		t.Fatalf("start = %+v", reports)
	}
	report(client.CommitProgress{FilesDone: 1, BytesSent: 100})
	report(client.CommitProgress{FilesDone: 1, BytesSent: 200}) // within the interval: dropped
	report(client.CommitProgress{FilesDone: 5, BytesSent: 99999})
	last := reports[len(reports)-1]
	if last.FilesDone != 2 || last.BytesSent != 1500 {
		t.Fatalf("final report %+v, want capped at the totals", last)
	}
	if len(reports) != 3 {
		t.Fatalf("reports = %d, want start, first and final (one throttled)", len(reports))
	}
	end()
	if reports[len(reports)-1] != nil {
		t.Fatal("the end of the publication was not reported")
	}
}

// Without a listener the commit gets no reporter and nothing to end.
func TestPublishProgressIsOptional(t *testing.T) {
	report, end := (&Service{}).startPublishProgress(nil, nil)
	if report != nil {
		t.Fatal("a reporter without a listener")
	}
	end()
}
