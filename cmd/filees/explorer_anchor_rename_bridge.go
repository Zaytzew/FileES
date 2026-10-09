//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type anchorRenameCallback struct {
	anchorDeleteCallback // identical OS object binding, NOT deletion permission
	TargetPath           string
}

var errAnchorRenameNotPrepared = errors.New("rename refused before durable preparation")

func parseAnchorRenameFrame(line string) (string, anchorRenameCallback, bool) {
	var cb anchorRenameCallback
	if len(line) > 12288 || !utf8.ValidString(line) || strings.ContainsAny(line, "\x00\r\n") {
		return "", cb, false
	}
	f := strings.Split(line, "\t")
	if len(f) != 11 || (f[0] != "rename-request" && f[0] != "rename-complete") {
		return "", cb, false
	}
	token := f[1]
	pre := f[0] == "rename-request"
	if (pre && !anchorDeleteToken(token)) || (!pre && token != "0") {
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
	if !a || !b || !c || !d || !g || e != nil || strconv.FormatInt(request, 10) != f[5] || root == 0 || file == 0 || file == root || process == 0 ||
		(pre && (flags != 7 || request == 0)) || (!pre && flags != 0) || len(f[7]) > 2048 || len(f[8]) > 4096 || len(f[10]) > 4096 ||
		!strings.HasPrefix(f[8], "\\") || !strings.HasPrefix(f[10], "\\") || f[8] == f[10] {
		return token, cb, false
	}
	rel, revision, size, ok := parseAnchorRemoteIdentity(f[7])
	canonical, err := anchorRemoteIdentity(rel, revision, size)
	if !ok || revision <= 0 || err != nil || canonical != f[7] {
		return token, cb, false
	}
	cb = anchorRenameCallback{anchorDeleteCallback{Volume: uint32(v), RootFileID: root, FileID: file, RequestKey: request, Flags: uint32(flags), ProcessID: uint32(process), Identity: f[7], NormalizedPath: f[8]}, f[10]}
	return token, cb, true
}

// One bounded gesture per provider. Only a successful retirement or proved
// pre-intent refusal rearms it. Completion and successful native ACK are both
// required, in either arrival order, without blocking the helper's reader.
type anchorRenameTransport struct {
	mu                                 sync.Mutex
	ctx                                context.Context
	run                                func(context.Context, anchorRenameCallback, func(context.Context) error) error
	work                               func(func())
	send                               func(string)
	token                              string
	cb                                 anchorRenameCallback
	approved, acked, completed, failed bool
	notice                             chan struct{}
}

func newAnchorRenameTransport(ctx context.Context, run func(context.Context, anchorRenameCallback, func(context.Context) error) error, work func(func()), send func(string)) *anchorRenameTransport {
	return &anchorRenameTransport{ctx: ctx, run: run, work: work, send: send}
}

func (r *anchorRenameTransport) notify() {
	select {
	case r.notice <- struct{}{}:
	default:
	}
}

func (r *anchorRenameTransport) receive(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.HasPrefix(line, "rename-acked\t") {
		if len(line) > 128 || strings.ContainsAny(line, "\x00\r\n") {
			return
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 || f[1] != r.token || !r.approved || r.failed {
			return
		}
		if f[2] == hresultOK {
			r.acked = true
		} else {
			r.failed = true
		}
		r.notify()
		return
	}
	token, cb, valid := parseAnchorRenameFrame(line)
	if strings.HasPrefix(line, "rename-complete\t") {
		cb.Session = r.cb.Session
		if valid && r.approved && sameAnchorDeleteObject(cb.anchorDeleteCallback, r.cb.anchorDeleteCallback) && cb.TargetPath == r.cb.TargetPath {
			r.completed = true
			r.notify()
		}
		return
	}
	if !valid || r.run == nil || r.ctx.Err() != nil || r.token != "" {
		if anchorDeleteToken(token) {
			r.send("rename-deny\t" + token + "\t1")
		}
		return
	}
	cb.Session = uuid.NewString()
	r.token, r.cb, r.notice = token, cb, make(chan struct{}, 1)
	r.work(func() {
		ctx, cancel := context.WithTimeout(r.ctx, 55*time.Second)
		defer cancel()
		err := r.run(ctx, cb, func(ctx context.Context) error {
			r.mu.Lock()
			if ctx.Err() != nil {
				r.mu.Unlock()
				return ctx.Err()
			}
			r.approved = true
			r.send("rename-allow\t" + token + "\t1")
			r.mu.Unlock()
			for {
				r.mu.Lock()
				ok, failed := r.acked && r.completed, r.failed
				r.mu.Unlock()
				if failed {
					return errors.New("native rename acknowledgement failed")
				}
				if ok {
					return nil
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-r.notice:
				}
			}
		})
		r.mu.Lock()
		defer r.mu.Unlock()
		if err != nil && !r.approved {
			r.send("rename-deny\t" + token + "\t1")
		}
		if err == nil || errors.Is(err, errAnchorRenameNotPrepared) {
			r.token, r.cb = "", anchorRenameCallback{}
			r.approved, r.acked, r.completed, r.failed = false, false, false, false
		}
	})
}
