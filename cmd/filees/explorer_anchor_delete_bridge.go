//go:build !nocfapi

package main

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type anchorDeleteCallback struct {
	Session                  string // supplied only by the owning connection
	Volume                   uint32
	RootFileID, FileID       uint64
	RequestKey               int64
	Flags, ProcessID         uint32
	Identity, NormalizedPath string
}

func sameAnchorDeleteObject(a, b anchorDeleteCallback) bool {
	// Completion RequestKey can be zero; flags are checked per phase.
	return a.Session == b.Session && a.Volume == b.Volume && a.RootFileID == b.RootFileID && a.FileID == b.FileID &&
		a.ProcessID == b.ProcessID && a.Identity == b.Identity && a.NormalizedPath == b.NormalizedPath
}

// One bounded operation per connection until its coordinator can safely retire
// it. No lifetime cache or goroutine per incoming frame. Permission, successful
// native ACK and completed OS deletion are three DIFFERENT pieces of evidence.
// Closing/timing out never fabricates completion or clears a durable intent.
type anchorDeleteTransport struct {
	mu                                sync.Mutex
	ctx                               context.Context
	session                           string
	prepare, complete                 func(context.Context, anchorDeleteCallback) error
	timeout                           time.Duration
	work                              func(func())
	send                              func(string)
	token                             string
	cb                                anchorDeleteCallback
	approved, acked, failed, finished bool
	completion                        *anchorDeleteCallback
}

func newAnchorDeleteTransport(ctx context.Context, prepare, complete func(context.Context, anchorDeleteCallback) error, timeout time.Duration, work func(func()), send func(string)) *anchorDeleteTransport {
	if timeout <= 0 || timeout > 8*time.Second {
		timeout = 8 * time.Second
	}
	return &anchorDeleteTransport{ctx: ctx, session: uuid.NewString(), prepare: prepare, complete: complete, timeout: timeout, work: work, send: send}
}

func anchorDeleteToken(s string) bool {
	n, err := strconv.ParseUint(s, 10, 31)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == s
}

func parseAnchorDeleteFrame(line string) (string, anchorDeleteCallback, bool) {
	var cb anchorDeleteCallback
	if len(line) > 8192 || !utf8.ValidString(line) || strings.ContainsAny(line, "\x00\r\n") {
		return "", cb, false
	}
	f := strings.Split(line, "\t")
	if len(f) != 10 ||
		(f[0] != "delete-request" && f[0] != "delete-complete") {
		return "", cb, false
	}
	token := f[1]
	if (f[0] == "delete-request" && !anchorDeleteToken(token)) || (f[0] == "delete-complete" && token != "0") {
		return "", cb, false
	}
	uintField := func(s string, bits int) (uint64, bool) {
		n, err := strconv.ParseUint(s, 10, bits)
		return n, err == nil && strconv.FormatUint(n, 10) == s
	}
	v, a := uintField(f[2], 32)
	root, b := uintField(f[3], 64)
	file, c := uintField(f[4], 64)
	request, e := strconv.ParseInt(f[5], 10, 64)
	flags, d := uintField(f[6], 32)
	process, g := uintField(f[9], 32)
	if !a || !b || !c || !d || !g || e != nil || strconv.FormatInt(request, 10) != f[5] || root == 0 || file == 0 || file == root || process == 0 || flags != 0 ||
		(f[0] == "delete-request" && request == 0) || len(f[7]) > 2048 || len(f[8]) > 4096 || !strings.HasPrefix(f[8], "\\") {
		return token, cb, false
	}
	rel, revision, size, ok := parseAnchorRemoteIdentity(f[7])
	canonical, err := anchorRemoteIdentity(rel, revision, size)
	if !ok || revision <= 0 || err != nil || canonical != f[7] {
		return token, cb, false
	}
	cb = anchorDeleteCallback{Volume: uint32(v), RootFileID: root, FileID: file, RequestKey: request, Flags: uint32(flags), ProcessID: uint32(process), Identity: f[7], NormalizedPath: f[8]}
	return token, cb, true
}

func (d *anchorDeleteTransport) receive(line string) {
	if strings.HasPrefix(line, "delete-acked\t") {
		if len(line) > 128 || strings.ContainsAny(line, "\x00\r\n") {
			return
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 || !anchorDeleteToken(f[1]) {
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if f[1] != d.token || !d.approved || d.failed || d.finished {
			return
		}
		if f[2] != hresultOK {
			d.failed = true
			return
		}
		d.acked = true
		d.finishLocked()
		return
	}
	token, cb, valid := parseAnchorDeleteFrame(line)
	pre := strings.HasPrefix(line, "delete-request\t")
	d.mu.Lock()
	defer d.mu.Unlock()
	if !pre {
		if !valid || !d.approved || d.failed || d.finished {
			return
		}
		cb.Session = d.session
		if !sameAnchorDeleteObject(cb, d.cb) {
			return
		}
		// Completion may precede the stdout report of CfExecute returning.
		if d.completion == nil {
			d.completion = &cb
		}
		d.finishLocked()
		return
	}
	if !valid || d.prepare == nil || d.complete == nil || d.ctx.Err() != nil || d.token != "" {
		// Even a malformed frame can only receive a typed refusal, never a
		// fetch answer or an echoed pathname used as a grant.
		if anchorDeleteToken(token) {
			d.send("delete-deny\t" + token + "\t1")
		}
		return
	}
	cb.Session = d.session
	d.token, d.cb = token, cb
	d.work(func() { d.authorize(token, cb) })
}

func (d *anchorDeleteTransport) authorize(token string, cb anchorDeleteCallback) {
	ctx, cancel := context.WithTimeout(d.ctx, d.timeout)
	defer cancel()
	err := d.prepare(ctx, cb)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil || ctx.Err() != nil {
		d.failed = true
		d.send("delete-deny\t" + token + "\t1")
		return
	}
	d.approved = true
	d.send("delete-allow\t" + token + "\t1")
	// A missing native ACK or completion leaves the journal fenced. It does
	// not consume an unbounded waiter or authorize another operation.
}

func (d *anchorDeleteTransport) finishLocked() {
	if !d.acked || d.completion == nil || d.finished || d.failed || d.ctx.Err() != nil {
		return
	}
	d.finished = true
	cb := *d.completion
	d.work(func() {
		// The durable coordinator owns reporting/recovery of this failure.
		if err := d.complete(d.ctx, cb); err != nil {
			d.mu.Lock()
			d.failed = true
			d.mu.Unlock()
		}
	})
}
