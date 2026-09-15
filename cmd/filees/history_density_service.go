package main

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/historyindex"
	"filees/pkg/ipcserver"
	"filees/pkg/shout"
)

func defaultHistoryIndexPath() string {
	return filepath.Join(filepath.Dir(clientprofile.DefaultRoot()), "history-index")
}

const (
	// A page of 100 revisions keeps one changed-path log inside the helper's
	// bounded output even with a large reorganisation among them.
	historyIndexPage = 100
	// One run indexes at most 25 000 revisions; an open window asking again
	// starts the next run, a closed window does not.
	historyIndexPagesPerRun = 250
	historyIndexRunTimeout  = 15 * time.Minute
	// historyIndexRecheck spaces HEAD checks for an index that is current, so
	// a polling window does not become a stream of helper calls.
	historyIndexRecheck = 30 * time.Second
)

// historyDensityService extends a repository's index in the background when
// the chart asks and answers from what is indexed so far. It holds no daemon
// admission: the run only reads the repository and appends to its own cache,
// and must not hold a restart back for a quarter of an hour.
type historyDensityService struct {
	index  *historyindex.Index
	source func(serverID, repoURL string) (historyindex.Source, error)
	now    func() time.Time

	mu    sync.Mutex
	runs  map[string]*historyIndexRun
	group sync.WaitGroup
}

type historyIndexRun struct {
	running bool
	current bool // the last run reached HEAD
	ended   time.Time
	head    int64
	failure string
}

func newHistoryDensityService(dir string, source func(string, string) (historyindex.Source, error)) *historyDensityService {
	return &historyDensityService{
		index:  &historyindex.Index{Dir: dir, Page: historyIndexPage},
		source: source,
		now:    time.Now,
		runs:   make(map[string]*historyIndexRun),
	}
}

func (d *historyDensityService) HistoryDensity(ctx context.Context, serverID, repoURL, uuid string, q historyindex.Query) (ipcserver.HistoryDensity, error) {
	d.ensureIndexing(serverID, repoURL, uuid)
	result, err := d.index.Aggregate(uuid, q)
	if err != nil {
		return ipcserver.HistoryDensity{}, err
	}
	d.mu.Lock()
	run := *d.runs[uuid]
	d.mu.Unlock()
	return ipcserver.HistoryDensity{Result: result, Head: run.head, Indexing: run.running, Diagnostic: run.failure}, nil
}

// ensureIndexing starts at most one run per repository. A run that spent its
// page budget is followed at once by the next request; one that reached HEAD
// or failed waits historyIndexRecheck.
func (d *historyDensityService) ensureIndexing(serverID, repoURL, uuid string) {
	d.mu.Lock()
	run := d.runs[uuid]
	if run == nil {
		run = &historyIndexRun{}
		d.runs[uuid] = run
	}
	quiet := (run.current || run.failure != "") && d.now().Sub(run.ended) < historyIndexRecheck
	if run.running || quiet {
		d.mu.Unlock()
		return
	}
	run.running = true
	d.mu.Unlock()

	d.group.Add(1)
	go func() {
		defer d.group.Done()
		var indexed, head int64
		var err error
		ctx, cancel := context.WithTimeout(context.Background(), historyIndexRunTimeout)
		defer cancel()
		var src historyindex.Source
		if src, err = d.source(serverID, repoURL); err == nil {
			indexed, head, err = d.index.Extend(ctx, uuid, src, historyIndexPagesPerRun)
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		run.running, run.ended, run.failure = false, d.now(), ""
		if head > 0 {
			run.head = head
		}
		run.current = err == nil && indexed == head
		if err != nil {
			run.failure = err.Error()
		}
	}()
}

// historyIndexSource reads a repository for the index through the native
// helper: HEAD by a dated resolve, pages by the changed-path log.
type historyIndexSource struct {
	reader client.HistoryReader
	url    string
}

func (s historyIndexSource) Head(ctx context.Context) (int64, error) {
	rev, _, err := s.reader.HistoryRevisionAt(ctx, s.url, time.Now().Add(time.Hour))
	return rev, err
}

func (s historyIndexSource) Log(ctx context.Context, newest, oldest int64, limit int) ([]historyindex.Commit, error) {
	commits, err := s.reader.HistoryLog(ctx, s.url, newest, oldest, limit)
	if err != nil {
		return nil, err
	}
	out := make([]historyindex.Commit, 0, len(commits))
	for _, commit := range commits {
		_, isShout := shout.Parse(commit.Message)
		entry := historyindex.Commit{Revision: commit.Revision, Date: commit.Date, Shout: isShout, Paths: make([]string, 0, len(commit.Changes))}
		for _, change := range commit.Changes {
			entry.Paths = append(entry.Paths, change.Path)
		}
		out = append(out, entry)
	}
	return out, nil
}

func (h historyService) densitySource(serverID, repoURL string) (historyindex.Source, error) {
	reader, err := h.reader(serverID)
	if err != nil {
		return nil, err
	}
	return historyIndexSource{reader: reader, url: repoURL}, nil
}
