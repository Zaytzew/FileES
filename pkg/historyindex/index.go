// Package historyindex keeps what the Wehikuł czasu chart needs from a
// repository's log (concepts/REPOSITORY_HISTORY_CONCEPT.md §4.1, §6, §7.1):
// for every revision its date, how many paths it changed, whether it carried a
// shout, and bounded hashes of those paths for distinct counts.
//
// Counting changed paths needs the changed-path log; the date and revision
// alone cannot give it. Asking the server for the whole log on every window
// open would repeat the most expensive read FileES does, so the index lives on
// disk, one append-only file per repository UUID, and is extended in pages from
// where it stopped. A new UUID is a new file: a repository replaced under the
// same URL never lends its bars to another.
//
// Nothing here scans on its own. The daemon extends an index when a window
// asks, within a page budget, so an idle daemon does no history work.
package historyindex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry is one indexed revision; field names are short because a long history
// holds many of them.
type Entry struct {
	Revision int64    `json:"r"`
	Unix     int64    `json:"t"`
	Changed  int      `json:"c"`
	Shout    bool     `json:"s,omitempty"`
	Paths    []uint64 `json:"p,omitempty"`
	// Capped marks an entry whose path hashes were not all kept.
	Capped bool `json:"x,omitempty"`
}

// Commit is what a Source reports for one revision.
type Commit struct {
	Revision int64
	Date     string // RFC 3339 as SVN sends it
	Shout    bool
	Paths    []string
}

// Source reads the repository. Log returns newest..oldest, newest first, like
// the daemon's history service.
type Source interface {
	Head(ctx context.Context) (int64, error)
	Log(ctx context.Context, newest, oldest int64, limit int) ([]Commit, error)
}

const (
	// MaxPathsPerEntry bounds one revision's hashes; a reorganisation of a
	// hundred thousand paths still counts all of them in Changed.
	MaxPathsPerEntry = 4096
	defaultPage      = 500
	defaultMaxBytes  = 256 << 20
	// uniqueBudget bounds the distinct-path sets one aggregation may hold.
	uniqueBudget = 2_000_000
)

var (
	ErrInvalidRepository = errors.New("history index: invalid repository identity")
	ErrInconsistent      = errors.New("history index: source is inconsistent with the index")
)

type Index struct {
	Dir string
	// Page is the number of revisions per log request; MaxBytes the size at
	// which an index stops keeping path hashes for new revisions.
	Page     int
	MaxBytes int64

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func validUUID(uuid string) bool {
	if len(uuid) == 0 || len(uuid) > 64 {
		return false
	}
	for _, r := range uuid {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' || r == '-') {
			return false
		}
	}
	return true
}

func (x *Index) lock(uuid string) *sync.Mutex {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.locks == nil {
		x.locks = make(map[string]*sync.Mutex)
	}
	if x.locks[uuid] == nil {
		x.locks[uuid] = &sync.Mutex{}
	}
	return x.locks[uuid]
}

func (x *Index) path(uuid string) string { return filepath.Join(x.Dir, uuid+".ndjson") }

// tail finds the end of the last complete line and that line. A crash while
// appending leaves a line without its newline; it is not an entry.
func tail(file *os.File) (Entry, int64, bool, error) {
	info, err := file.Stat()
	if err != nil {
		return Entry{}, 0, false, err
	}
	end, err := lastNewline(file, info.Size())
	if err != nil || end < 0 {
		return Entry{}, 0, false, err
	}
	start, err := lastNewline(file, end)
	if err != nil {
		return Entry{}, 0, false, err
	}
	line := make([]byte, end-(start+1))
	if _, err := file.ReadAt(line, start+1); err != nil {
		return Entry{}, 0, false, err
	}
	var entry Entry
	if err := json.Unmarshal(line, &entry); err != nil {
		return Entry{}, 0, false, fmt.Errorf("history index: damaged last entry: %w", err)
	}
	return entry, end + 1, true, nil
}

// lastNewline returns the offset of the last '\n' before limit, or -1.
func lastNewline(file *os.File, limit int64) (int64, error) {
	buf := make([]byte, 64<<10)
	for limit > 0 {
		size := int64(len(buf))
		if limit < size {
			size = limit
		}
		chunk := buf[:size]
		if _, err := file.ReadAt(chunk, limit-size); err != nil && !errors.Is(err, io.EOF) {
			return -1, err
		}
		if i := bytes.LastIndexByte(chunk, '\n'); i >= 0 {
			return limit - size + int64(i), nil
		}
		limit -= size
	}
	return -1, nil
}

// Indexed is the last revision the index holds; 0 for none.
func (x *Index) Indexed(uuid string) (int64, error) {
	if !validUUID(uuid) {
		return 0, ErrInvalidRepository
	}
	l := x.lock(uuid)
	l.Lock()
	defer l.Unlock()
	file, err := os.Open(x.path(uuid))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer file.Close()
	last, _, ok, err := tail(file)
	if err != nil || !ok {
		return 0, err
	}
	return last.Revision, nil
}

func hashPath(path string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(path))
	return h.Sum64()
}

// Extend appends revisions after the last indexed one, at most pages log
// requests, and reports how far the index and the repository reach.
func (x *Index) Extend(ctx context.Context, uuid string, src Source, pages int) (indexed, head int64, err error) {
	if !validUUID(uuid) {
		return 0, 0, ErrInvalidRepository
	}
	l := x.lock(uuid)
	l.Lock()
	defer l.Unlock()
	if err := os.MkdirAll(x.Dir, 0700); err != nil {
		return 0, 0, err
	}
	file, err := os.OpenFile(x.path(uuid), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	last, goodEnd, _, err := tail(file)
	if err != nil {
		return 0, 0, err
	}
	if err := file.Truncate(goodEnd); err != nil {
		return 0, 0, err
	}
	indexed = last.Revision
	if head, err = src.Head(ctx); err != nil {
		return indexed, 0, err
	}
	if head < indexed {
		return indexed, head, fmt.Errorf("%w: HEAD r%d is behind the indexed r%d", ErrInconsistent, head, indexed)
	}
	page, maxBytes := x.Page, x.MaxBytes
	if page <= 0 {
		page = defaultPage
	}
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	size := goodEnd
	for n := 0; n < pages && indexed < head; n++ {
		if err := ctx.Err(); err != nil {
			return indexed, head, err
		}
		oldest := indexed + 1
		newest := min(indexed+int64(page), head)
		commits, err := src.Log(ctx, newest, oldest, int(newest-oldest+1))
		if err != nil {
			return indexed, head, err
		}
		// Every revision changes the repository root, so a root log over a
		// range must answer every revision in it, newest first, without gaps.
		if int64(len(commits)) != newest-oldest+1 {
			return indexed, head, fmt.Errorf("%w: log r%d:%d answered %d revisions", ErrInconsistent, newest, oldest, len(commits))
		}
		var lines bytes.Buffer
		for i := len(commits) - 1; i >= 0; i-- {
			commit := commits[i]
			if commit.Revision != oldest+int64(len(commits)-1-i) {
				return indexed, head, fmt.Errorf("%w: log r%d:%d out of order at r%d", ErrInconsistent, newest, oldest, commit.Revision)
			}
			at, err := time.Parse(time.RFC3339Nano, commit.Date)
			if err != nil {
				return indexed, head, fmt.Errorf("%w: r%d has no usable date", ErrInconsistent, commit.Revision)
			}
			entry := Entry{Revision: commit.Revision, Unix: at.Unix(), Changed: len(commit.Paths), Shout: commit.Shout}
			if size < maxBytes {
				for _, path := range commit.Paths {
					if len(entry.Paths) == MaxPathsPerEntry {
						entry.Capped = true
						break
					}
					entry.Paths = append(entry.Paths, hashPath(path))
				}
			} else if len(commit.Paths) > 0 {
				entry.Capped = true
			}
			line, err := json.Marshal(entry)
			if err != nil {
				return indexed, head, err
			}
			lines.Write(line)
			lines.WriteByte('\n')
		}
		if _, err := file.WriteAt(lines.Bytes(), size); err != nil {
			_ = file.Truncate(size)
			return indexed, head, err
		}
		if err := file.Sync(); err != nil {
			return indexed, head, err
		}
		size += int64(lines.Len())
		indexed = newest
	}
	return indexed, head, nil
}

// Query selects and groups indexed revisions. Buckets are aligned to the
// user's zone so that a 24-hour bar is a local day.
type Query struct {
	BucketSeconds int64
	OffsetSeconds int64
	// From and To bound commit times, inclusive; zero leaves a side open.
	From, To time.Time
	// After continues a page: only buckets starting after it (Unix seconds).
	After int64
	Limit int
}

type Bucket struct {
	Start        int64 `json:"start"`
	End          int64 `json:"end"`
	ChangedPaths int64 `json:"changed_paths"`
	UniquePaths  int64 `json:"unique_paths"`
	UniqueExact  bool  `json:"unique_exact"`
	Commits      int64 `json:"commits"`
	Shouts       int64 `json:"shouts"`
}

type Result struct {
	FirstUnix int64
	LastUnix  int64
	Indexed   int64
	Buckets   []Bucket
	// More is true when Limit cut the buckets; continue with After = last Start.
	More bool
}

type bucketState struct {
	Bucket
	seen map[uint64]struct{}
}

// Aggregate groups every indexed revision; nothing is sampled or dropped.
func (x *Index) Aggregate(uuid string, q Query) (Result, error) {
	if !validUUID(uuid) {
		return Result{}, ErrInvalidRepository
	}
	if q.BucketSeconds < 60 || q.OffsetSeconds <= -14*3600 || q.OffsetSeconds >= 14*3600+1 || q.Limit < 0 {
		return Result{}, errors.New("history index: invalid query")
	}
	l := x.lock(uuid)
	l.Lock()
	defer l.Unlock()
	file, err := os.Open(x.path(uuid))
	if errors.Is(err, os.ErrNotExist) {
		return Result{Buckets: []Bucket{}}, nil
	}
	if err != nil {
		return Result{}, err
	}
	defer file.Close()

	var result Result
	buckets := make(map[int64]*bucketState)
	budget := uniqueBudget
	exact := true
	reader := bufio.NewReaderSize(file, 1<<20)
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break // a line without newline is an interrupted append, not an entry
		}
		if err != nil {
			return Result{}, err
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			return Result{}, fmt.Errorf("history index: damaged entry: %w", err)
		}
		result.Indexed = entry.Revision
		if result.FirstUnix == 0 || entry.Unix < result.FirstUnix {
			result.FirstUnix = entry.Unix
		}
		if entry.Unix > result.LastUnix {
			result.LastUnix = entry.Unix
		}
		if (!q.From.IsZero() && entry.Unix < q.From.Unix()) || (!q.To.IsZero() && entry.Unix > q.To.Unix()) {
			continue
		}
		local := entry.Unix + q.OffsetSeconds
		start := floorDiv(local, q.BucketSeconds)*q.BucketSeconds - q.OffsetSeconds
		if start <= q.After && q.After != 0 {
			continue
		}
		b := buckets[start]
		if b == nil {
			b = &bucketState{Bucket: Bucket{Start: start, End: start + q.BucketSeconds, UniqueExact: true}, seen: map[uint64]struct{}{}}
			buckets[start] = b
		}
		b.Commits++
		b.ChangedPaths += int64(entry.Changed)
		if entry.Shout {
			b.Shouts++
		}
		if entry.Capped || len(entry.Paths) < entry.Changed {
			b.UniqueExact = false
		}
		for _, hash := range entry.Paths {
			if _, dup := b.seen[hash]; dup {
				continue
			}
			if budget == 0 {
				exact = false
				b.UniqueExact = false
				break
			}
			b.seen[hash] = struct{}{}
			budget--
		}
	}
	result.Buckets = make([]Bucket, 0, len(buckets))
	for _, b := range buckets {
		b.UniquePaths = int64(len(b.seen))
		// Once the shared budget ran out no bucket can prove its count.
		if !exact {
			b.UniqueExact = false
		}
		result.Buckets = append(result.Buckets, b.Bucket)
	}
	sort.Slice(result.Buckets, func(i, j int) bool { return result.Buckets[i].Start < result.Buckets[j].Start })
	if q.Limit > 0 && len(result.Buckets) > q.Limit {
		result.Buckets, result.More = result.Buckets[:q.Limit], true
	}
	return result, nil
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}
