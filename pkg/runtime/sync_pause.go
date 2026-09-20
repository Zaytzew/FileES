package runtime

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrSyncPaused = errors.New("synchronization is paused")
var ErrDraftExpired = errors.New("announcement editor lease is no longer active")

const DraftTTL = time.Minute

type draftContextKey struct{}

func WithDraft(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, draftContextKey{}, token)
}
func DraftToken(ctx context.Context) string {
	token, _ := ctx.Value(draftContextKey{}).(string)
	return token
}

// SyncPause is shared by every local sync worker. Manual pause lives until
// resume or daemon restart; an editor lease expires if its GUI disappears.
// Already admitted operations finish. A draft becomes ready only after all
// admitted commits finish; its token is bound to one repository.
// Zero value is ready. No timers or workers survive a discarded gate.
type SyncPause struct {
	mu             sync.Mutex
	manual         bool
	token, repo    string
	until          time.Time
	commits, polls int
	publishing     bool
	now            func() time.Time
}

func (p *SyncPause) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}
func (p *SyncPause) expire() {
	if p.token != "" && !p.clock().Before(p.until) {
		p.token, p.repo = "", ""
	}
}
func (p *SyncPause) SetManual(paused bool) { p.mu.Lock(); defer p.mu.Unlock(); p.manual = paused }
func (p *SyncPause) Status() (manual, draft, draining bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expire()
	return p.manual, p.token != "" || p.publishing, (p.manual && p.polls > 0) || ((p.manual || p.token != "") && p.commits > 0)
}
func (p *SyncPause) Draft(token, repo, action string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expire()
	if token == "" || repo == "" {
		return false, ErrDraftExpired
	}
	switch action {
	case "begin":
		if p.publishing || (p.token != "" && (p.token != token || p.repo != repo)) {
			return false, ErrSyncPaused
		}
		p.token, p.repo, p.until = token, repo, p.clock().Add(DraftTTL)
	case "renew":
		if p.token != token || p.repo != repo {
			return false, ErrDraftExpired
		}
		p.until = p.clock().Add(DraftTTL)
	case "end":
		if p.token == token && p.repo == repo {
			p.token, p.repo = "", ""
		}
		return true, nil
	default:
		return false, ErrDraftExpired
	}
	return p.commits == 0, nil
}

// Enter fences a full commit or polling cycle, including queue changes.
// A paused background caller must leave its queue untouched and try later.
func (p *SyncPause) Enter(ctx context.Context, repo string, commit bool) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil {
		return func() {}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expire()
	token := DraftToken(ctx)
	own := token != ""
	if own && (!commit || p.token != token || p.repo != repo || p.commits != 0) {
		return nil, ErrDraftExpired
	}
	if p.manual || (commit && (p.publishing || (p.token != "" && !own))) {
		return nil, ErrSyncPaused
	}
	if commit {
		p.commits++
	} else {
		p.polls++
	}
	if own {
		p.publishing = true
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if commit {
				p.commits--
			} else {
				p.polls--
			}
			if own {
				p.publishing = false
			}
		})
	}, nil
}
