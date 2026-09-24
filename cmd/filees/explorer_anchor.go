//go:build !nocfapi

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"filees/pkg/talk"
)

// The daemon's half of the Explorer anchor (native/filees-cfapi/README.md).
//
// The helper knows the shell and nothing else. When someone opens a
// placeholder it asks, in one line, for the bytes behind an identity (a path
// in the repository). The order here was measured against real Subversion
// (implementation notes (not distributed)):
//
//  1. fetch: the file at a pinned revision is written to a temporary file and
//     that path goes back; the helper hands the bytes to Windows and the
//     application's open completes.
//  2. hydrated: the placeholder is now whole on disk. Only now may Subversion
//     read it - reading a partial placeholder would ask for it again - so only
//     now is the path adopted into the working copy at the same revision.
//  3. revert: the helper, being the connected provider, turns the placeholder
//     into an ordinary file of the working copy. An application holding the
//     file makes Windows refuse; the daemon asks again later.
//
// Two rules decide the shape of this file:
//
//   - the helper's output must be drained continuously and never written to
//     from the reading loop. A blocked pipe stops its callbacks, and every
//     operation in Explorer then waits out the filter's two minute timeout.
//   - fetching, adopting and reverting take as long as they take; each runs in
//     its own goroutine, so a slow one never delays the next request.
type anchorBridge struct {
	// root is the anchor folder; identities are paths below it.
	root string
	// materialize writes the file at identity to a temporary file and says at
	// which revision it read it.
	materialize func(ctx context.Context, identity string) (file string, revision int64, err error)
	// adopt takes identity into the working copy at revision, adopting the
	// bytes already on disk. Nil in tests that stop at the fetch.
	adopt func(ctx context.Context, identity string, revision int64) error
	guard *anchorGuard
	log   talk.Logger

	fetchMu   sync.Mutex
	revisions map[string]anchorFetched

	// Answers are queued, never written from the reading loop. Writing there
	// is the same mistake as not reading at all. The queue is unbounded on
	// purpose - a fetch in flight is bounded by the filter, and dropping an
	// answer would leave a click hanging.
	queueMu sync.Mutex
	queued  []string
	ready   *sync.Cond
	closed  bool

	// revertRetry is how long to wait before asking again for a file an
	// application still holds. Tests shorten it.
	revertRetry time.Duration
}

type anchorFetched struct {
	identity string
	revision int64
	reverts  int
}

var errAnchorHelperGone = errors.New("explorer anchor helper stopped")

// The helper answers these when asked to revert; both mean "done".
const (
	hresultOK            = "0x00000000"
	hresultNotACloudFile = "0x80070178"
	hresultInUse         = "0x8007018b"
	anchorRevertAttempts = 60
)

// anchorHelperStreams is how a helper process is started. Tests hand over
// plain pipes; the daemon hands over the real process.
type anchorHelperStreams struct {
	stdout io.Reader
	stdin  io.WriteCloser
	wait   func() error
}

func startAnchorHelper(ctx context.Context, helper, root string) (*anchorHelperStreams, error) {
	if !filepath.IsAbs(helper) || !filepath.IsAbs(root) {
		return nil, errors.New("explorer anchor needs absolute helper and anchor paths")
	}
	command := exec.CommandContext(ctx, helper, "connect", "--root", root)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &anchorHelperStreams{stdout: stdout, stdin: stdin, wait: command.Wait}, nil
}

// serve reads the helper's lines until it stops. It returns when the helper
// ends, which is also how the daemon takes an anchor offline: close stdin.
func (bridge *anchorBridge) serve(ctx context.Context, streams *anchorHelperStreams) error {
	bridge.ready = sync.NewCond(&bridge.queueMu)
	if bridge.revertRetry <= 0 {
		bridge.revertRetry = 5 * time.Second
	}
	var writing sync.WaitGroup
	writing.Add(1)
	go func() {
		defer writing.Done()
		bridge.writeAnswers(streams.stdin)
	}()

	var pending sync.WaitGroup
	work := func(run func()) {
		pending.Add(1)
		go func() {
			defer pending.Done()
			run()
		}()
	}
	reader := bufio.NewScanner(streams.stdout)
	reader.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for reader.Scan() {
		line := strings.TrimSpace(reader.Text())
		switch {
		case strings.HasPrefix(line, "fetch\t"):
			request, ok := parseAnchorFetch(line)
			if !ok {
				bridge.answer(request.id, "", errors.New("malformed fetch request"))
				continue
			}
			if refusal := bridge.guard.allow(request.process, request.identity, time.Now()); refusal != nil {
				bridge.answer(request.id, "", refusal)
				continue
			}
			work(func() { bridge.handle(ctx, request) })
		case strings.HasPrefix(line, "hydrated\t"):
			fields := strings.Split(line, "\t")
			if len(fields) == 3 {
				work(func() { bridge.hydrated(ctx, fields[1]) })
			}
		case strings.HasPrefix(line, "reverted\t"):
			fields := strings.Split(line, "\t")
			if len(fields) == 3 {
				work(func() { bridge.reverted(ctx, fields[1], fields[2]) })
			}
		default:
			// The helper's own first line ({"ok":true,...}) and anything else
			// that is not part of the protocol stays out of the way: this is a
			// protocol between two programs, not a place to guess.
		}
	}
	// Answers still in flight are written to a stream that may already be
	// closed; that is harmless, and waiting for them keeps the goroutines from
	// outliving the anchor.
	pending.Wait()
	bridge.queueMu.Lock()
	bridge.closed = true
	bridge.ready.Broadcast()
	bridge.queueMu.Unlock()
	writing.Wait()
	_ = streams.stdin.Close()
	if err := reader.Err(); err != nil {
		return err
	}
	if streams.wait != nil {
		if err := streams.wait(); err != nil {
			return fmt.Errorf("%w: %v", errAnchorHelperGone, err)
		}
	}
	return errAnchorHelperGone
}

type anchorFetch struct {
	id       string
	offset   int64
	length   int64
	identity string
	process  string
}

func parseAnchorFetch(line string) (anchorFetch, bool) {
	fields := strings.Split(line, "\t")
	if len(fields) != 5 && len(fields) != 6 {
		return anchorFetch{}, false
	}
	request := anchorFetch{id: fields[1], identity: fields[4]}
	if len(fields) == 6 {
		request.process = fields[5]
	}
	offset, offsetErr := strconv.ParseInt(fields[2], 10, 64)
	length, lengthErr := strconv.ParseInt(fields[3], 10, 64)
	request.offset, request.length = offset, length
	if request.id == "" || offsetErr != nil || lengthErr != nil || offset < 0 || length < 0 || request.identity == "" {
		return request, false
	}
	return request, true
}

func (bridge *anchorBridge) handle(ctx context.Context, request anchorFetch) {
	if bridge.materialize == nil {
		bridge.answer(request.id, "", errors.New("materialization unavailable"))
		return
	}
	file, revision, err := bridge.materialize(ctx, request.identity)
	if err == nil && !filepath.IsAbs(file) {
		err = fmt.Errorf("materialized path %q is not absolute", file)
	}
	if err == nil {
		bridge.fetchMu.Lock()
		if bridge.revisions == nil {
			bridge.revisions = map[string]anchorFetched{}
		}
		bridge.revisions[request.id] = anchorFetched{identity: request.identity, revision: revision}
		bridge.fetchMu.Unlock()
	}
	bridge.answer(request.id, file, err)
}

// hydrated: the file is whole on disk. Adopt it at the revision it was read
// at, then ask the helper to turn the placeholder into an ordinary file.
func (bridge *anchorBridge) hydrated(ctx context.Context, id string) {
	bridge.fetchMu.Lock()
	fetched, ok := bridge.revisions[id]
	bridge.fetchMu.Unlock()
	if !ok || bridge.adopt == nil {
		return
	}
	if err := bridge.adopt(ctx, fetched.identity, fetched.revision); err != nil {
		// The file stays a hydrated placeholder: readable, not yet part of the
		// copy. The next open of it is served from disk without asking.
		bridge.log.Warnf("explorer anchor: adopting %s at r%d: %v", fetched.identity, fetched.revision, err)
		return
	}
	bridge.sendRevert(id, fetched.identity)
}

func (bridge *anchorBridge) sendRevert(id, identity string) {
	path := filepath.Join(bridge.root, filepath.FromSlash(identity))
	bridge.enqueue("revert\t" + id + "\t" + path)
}

// reverted: done, or the file is held by an application - ask again later,
// for as long as a person plausibly keeps a drawing open, then give up
// quietly. A placeholder left hydrated is still readable and still ours.
func (bridge *anchorBridge) reverted(ctx context.Context, id, hresult string) {
	bridge.fetchMu.Lock()
	fetched, ok := bridge.revisions[id]
	done := hresult == hresultOK || hresult == hresultNotACloudFile
	if ok && (done || fetched.reverts >= anchorRevertAttempts) {
		delete(bridge.revisions, id)
	} else if ok {
		fetched.reverts++
		bridge.revisions[id] = fetched
	}
	bridge.fetchMu.Unlock()
	if !ok || done {
		return
	}
	if fetched.reverts >= anchorRevertAttempts {
		bridge.log.Warnf("explorer anchor: %s stays a placeholder (%s)", fetched.identity, hresult)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(bridge.revertRetry):
		bridge.sendRevert(id, fetched.identity)
	}
}

// answer queues the reply to one fetch. A reason with a tab or a newline in
// it would split into fields the helper reads as another answer, so it is
// flattened here rather than trusted.
func (bridge *anchorBridge) answer(id, local string, failure error) {
	if id == "" {
		return
	}
	line := "ok\t" + id + "\t" + local
	if failure != nil {
		bridge.log.Warnf("explorer anchor fetch %s: %v", id, failure)
		line = "err\t" + id + "\t" + strings.ReplaceAll(strings.ReplaceAll(failure.Error(), "\t", " "), "\n", " ")
	}
	bridge.enqueue(line)
}

func (bridge *anchorBridge) enqueue(line string) {
	bridge.queueMu.Lock()
	defer bridge.queueMu.Unlock()
	if bridge.closed {
		return
	}
	bridge.queued = append(bridge.queued, line)
	bridge.ready.Signal()
}

// writeAnswers is the only writer to the helper, so two lines never
// interleave into one it cannot read.
func (bridge *anchorBridge) writeAnswers(writer io.Writer) {
	for {
		bridge.queueMu.Lock()
		for len(bridge.queued) == 0 && !bridge.closed {
			bridge.ready.Wait()
		}
		if len(bridge.queued) == 0 {
			bridge.queueMu.Unlock()
			return
		}
		line := bridge.queued[0]
		bridge.queued = bridge.queued[1:]
		bridge.queueMu.Unlock()
		if _, err := io.WriteString(writer, line+"\n"); err != nil {
			return
		}
	}
}

// anchorGuard stops one program from downloading a whole anchor: a scanner,
// an indexer or a search walking the folder opens every file, and here every
// open is a Subversion checkout. A person opening drawings asks for a few;
// past the limit within the window, that program is refused for the rest of
// the anchor's session. Refused means an error for the program, never an
// empty file. (Owner's decision 2026-09-23; the question to the person is a
// later step - this is the floor.)
type anchorGuard struct {
	limit  int
	window time.Duration
	log    talk.Logger

	mu      sync.Mutex
	recent  map[string][]anchorAsk
	refused map[string]bool
}

type anchorAsk struct {
	identity string
	at       time.Time
}

func newAnchorGuard(log talk.Logger) *anchorGuard {
	return &anchorGuard{limit: 20, window: time.Minute, log: log}
}

func (guard *anchorGuard) allow(process, identity string, now time.Time) error {
	if guard == nil {
		return nil
	}
	if process == "" {
		process = "?"
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.recent == nil {
		guard.recent, guard.refused = map[string][]anchorAsk{}, map[string]bool{}
	}
	if guard.refused[process] {
		return fmt.Errorf("%s may not download from this anchor (too many files at once)", filepath.Base(process))
	}
	kept := guard.recent[process][:0]
	distinct := map[string]bool{identity: true}
	for _, ask := range guard.recent[process] {
		if now.Sub(ask.at) < guard.window {
			kept = append(kept, ask)
			distinct[ask.identity] = true
		}
	}
	guard.recent[process] = append(kept, anchorAsk{identity: identity, at: now})
	if len(distinct) > guard.limit {
		guard.refused[process] = true
		guard.log.Warnf("explorer anchor: %s asked for %d files within %s; refusing further downloads for it", process, len(distinct), guard.window)
		return fmt.Errorf("%s may not download from this anchor (too many files at once)", filepath.Base(process))
	}
	return nil
}
