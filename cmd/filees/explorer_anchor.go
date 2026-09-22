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

	"filees/pkg/talk"
)

// The daemon's half of the Explorer anchor (native/filees-cfapi/README.md).
//
// The helper knows the shell and nothing else: when someone opens a
// placeholder it asks, in one line, for the path behind an identity. This
// side answers by bringing that path onto the disk through the ordinary
// materialization - the same one the "Przeglądaj na serwerze" window uses -
// and replies with the file to read.
//
// Two rules decide the shape of this file:
//
//   - the helper's output must be drained continuously. A blocked write stops
//     its callbacks, and then every operation in Explorer waits out the
//     filter's two minute timeout instead of failing or succeeding.
//   - materializing takes as long as Subversion takes. It therefore happens
//     off the reading loop, one goroutine per request, so a slow path never
//     delays the next one.
type anchorBridge struct {
	// materialize turns a repository path into a file on this disk. It is the
	// only thing this bridge knows how to do, and it does not know how.
	materialize func(ctx context.Context, path string) (string, error)
	log         talk.Logger

	// Answers are queued, never written from the reading loop. Writing there
	// is the same mistake as not reading at all: the helper's pipe fills, the
	// loop stops, and the shell waits out the filter's timeout. The queue is
	// unbounded on purpose - a fetch in flight is bounded by the filter, and
	// dropping an answer would leave a click hanging.
	queueMu sync.Mutex
	queued  []string
	ready   *sync.Cond
	closed  bool
}

var errAnchorHelperGone = errors.New("explorer anchor helper stopped")

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
	var writing sync.WaitGroup
	writing.Add(1)
	go func() {
		defer writing.Done()
		bridge.writeAnswers(streams.stdin)
	}()

	var pending sync.WaitGroup
	reader := bufio.NewScanner(streams.stdout)
	reader.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for reader.Scan() {
		line := strings.TrimSpace(reader.Text())
		if line == "" {
			continue
		}
		// The first line is the helper's own answer ({"ok":true,...}), and
		// anything else that is not a request stays out of the way: this is a
		// protocol between two programs, not a place to guess.
		if !strings.HasPrefix(line, "fetch\t") {
			continue
		}
		request, ok := parseAnchorFetch(line)
		if !ok {
			bridge.answer(request.id, "", errors.New("malformed fetch request"))
			continue
		}
		pending.Add(1)
		go func() {
			defer pending.Done()
			bridge.handle(ctx, request)
		}()
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
}

func parseAnchorFetch(line string) (anchorFetch, bool) {
	fields := strings.Split(line, "\t")
	if len(fields) != 5 {
		return anchorFetch{}, false
	}
	request := anchorFetch{id: fields[1], identity: fields[4]}
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
	local, err := bridge.materialize(ctx, request.identity)
	if err == nil && !filepath.IsAbs(local) {
		err = fmt.Errorf("materialized path %q is not absolute", local)
	}
	bridge.answer(request.id, local, err)
}

// answer queues one line. A reason with a tab or a newline in it would split
// into fields the helper reads as another answer, so it is flattened here
// rather than trusted.
func (bridge *anchorBridge) answer(id, local string, failure error) {
	if id == "" {
		return
	}
	line := "ok\t" + id + "\t" + local
	if failure != nil {
		bridge.log.Warnf("explorer anchor fetch %s: %v", id, failure)
		line = "err\t" + id + "\t" + strings.ReplaceAll(strings.ReplaceAll(failure.Error(), "\t", " "), "\n", " ")
	}
	bridge.queueMu.Lock()
	defer bridge.queueMu.Unlock()
	if bridge.closed {
		return
	}
	bridge.queued = append(bridge.queued, line)
	bridge.ready.Signal()
}

// writeAnswers is the only writer to the helper, so two answers never
// interleave into a line it cannot read.
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
