package app

import (
	"context"
	"sync"
	"time"

	contract "filees/pkg/contract/v1"
)

// Clock abstracts time so tests can control timers without sleeping.
type Clock interface {
	AfterFunc(d time.Duration, f func()) clockTimer
}

type clockTimer interface {
	Stop() bool
}

type realClock struct{}

func (realClock) AfterFunc(d time.Duration, f func()) clockTimer { return time.AfterFunc(d, f) }

// BackoffSequence returns successive wait durations for reconnect attempts.
type BackoffSequence interface {
	Next() time.Duration
	Reset()
}

// ExponentialBackoff steps through a fixed series of durations, then holds the last.
type ExponentialBackoff struct {
	steps []time.Duration
	idx   int
	mu    sync.Mutex
}

// DefaultBackoff returns the backoff from gui-assumptions: 1s→2s→5s→10s→30s.
func DefaultBackoff() *ExponentialBackoff {
	return &ExponentialBackoff{
		steps: []time.Duration{
			1 * time.Second, 2 * time.Second, 5 * time.Second,
			10 * time.Second, 30 * time.Second,
		},
	}
}

func (b *ExponentialBackoff) Next() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	d := b.steps[b.idx]
	if b.idx < len(b.steps)-1 {
		b.idx++
	}
	return d
}

func (b *ExponentialBackoff) Reset() {
	b.mu.Lock()
	b.idx = 0
	b.mu.Unlock()
}

// Config holds all dependencies injected into the App.
type Config struct {
	Client   DaemonClient
	OnChange func(ViewModel) // called from event loop on every state change

	// Overridable for tests.
	Clock    Clock
	Backoff  BackoffSequence
	Debounce time.Duration // event coalescing window; default 150ms
	Periodic time.Duration // periodic snapshot refresh; default 30s
}

// App is the GUI presentation model. Call Run to start the event loop.
// All state mutations happen in the event loop goroutine.
type App struct {
	cfg   Config
	msgCh chan appMsg
	once  sync.Once
}

// New creates an App. cfg.Client and cfg.OnChange must be non-nil.
func New(cfg Config) *App {
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.Backoff == nil {
		cfg.Backoff = DefaultBackoff()
	}
	if cfg.Debounce == 0 {
		cfg.Debounce = 150 * time.Millisecond
	}
	if cfg.Periodic == 0 {
		cfg.Periodic = 30 * time.Second
	}
	return &App{cfg: cfg, msgCh: make(chan appMsg, 64)}
}

// Run starts the event loop. Blocks until ctx is cancelled. Safe to call once.
func (a *App) Run(ctx context.Context) {
	a.once.Do(func() { a.loop(ctx) })
}

// --- message types (sealed; only sent to msgCh) ---

type appMsg interface{ sealed() }

// msgConnected signals a successful hello+subscribe. gen identifies the session.
type msgConnected struct {
	gen  int
	caps []string
}

// msgDisconnected signals stream close or init failure. gen identifies the session.
type msgDisconnected struct{ gen int }

// msgReconnect is sent by the AfterFunc callback so connect() runs in the event loop.
type msgReconnect struct{}

type msgRepoList struct{ summaries []contract.RepoSummary }
type msgSnapshot struct{ status contract.RepoStatus }
type msgEvent struct{ ev contract.Event }
type msgFlushDirty struct{}

func (msgConnected) sealed()    {}
func (msgDisconnected) sealed() {}
func (msgReconnect) sealed()    {}
func (msgRepoList) sealed()     {}
func (msgSnapshot) sealed()     {}
func (msgEvent) sealed()        {}
func (msgFlushDirty) sealed()   {}

// --- event loop ---

func (a *App) loop(ctx context.Context) {
	state := newAppState()

	// Session tracking — only touched in this goroutine.
	var (
		connectGen    int                  // incremented on each connect() call
		currentCancel context.CancelFunc  // cancels the current session's context
		currentSesCtx context.Context     // passed to snapshot goroutines; nil when disconnected
	)

	var (
		dirtyRepos    = make(map[string]bool)
		debounceTimer clockTimer
	)

	periodic := time.NewTicker(a.cfg.Periodic)
	defer periodic.Stop()

	send := func(m appMsg) {
		select {
		case a.msgCh <- m:
		case <-ctx.Done():
		}
	}

	notify := func() {
		if a.cfg.OnChange != nil {
			a.cfg.OnChange(state.viewModel())
		}
	}

	// fullResync fetches repo.list then all repo.status in the current session.
	// Captures currentSesCtx/connectGen at call time — safe because called only from loop.
	fullResync := func() {
		sesCtx := currentSesCtx
		gen := connectGen
		if sesCtx == nil {
			return
		}
		go func() {
			list, err := a.cfg.Client.RepoList(sesCtx)
			if err != nil || sesCtx.Err() != nil {
				return
			}
			send(msgRepoList{list.Repos})
			for _, r := range list.Repos {
				id := r.ID
				go func() {
					s, err := a.cfg.Client.RepoStatus(sesCtx, id)
					if err != nil || sesCtx.Err() != nil {
						return
					}
					// Discard if session changed while in flight.
					if gen == connectGen {
						send(msgSnapshot{*s})
					}
				}()
			}
		}()
	}

	// connect is called ONLY from the event loop goroutine.
	// It creates a new session, cancels any previous one, and launches an init goroutine.
	connect := func() {
		connectGen++
		gen := connectGen

		sesCtx, sesCancel := context.WithCancel(ctx)
		if currentCancel != nil {
			currentCancel()
		}
		currentCancel = sesCancel
		currentSesCtx = sesCtx

		go func() {
			hello, err := a.cfg.Client.Hello(sesCtx)
			if err != nil {
				sesCancel()
				send(msgDisconnected{gen})
				return
			}

			// Subscribe before snapshots: events during init are not missed.
			var evCh <-chan contract.Event
			if hasCap(hello.Capabilities, contract.CapEventsSubscribe) {
				evCh, err = a.cfg.Client.Subscribe(sesCtx)
				if err != nil {
					sesCancel()
					send(msgDisconnected{gen})
					return
				}
			}

			send(msgConnected{gen, hello.Capabilities})

			if evCh != nil {
				go func() {
					a.drainEvents(sesCtx, evCh, send)
					// Stream closed naturally — sesCtx not yet cancelled by loop.
					if sesCtx.Err() == nil {
						send(msgDisconnected{gen})
					}
				}()
			}

			list, err := a.cfg.Client.RepoList(sesCtx)
			if err != nil || sesCtx.Err() != nil {
				return
			}
			send(msgRepoList{list.Repos})
			for _, r := range list.Repos {
				id := r.ID
				go func() {
					s, err := a.cfg.Client.RepoStatus(sesCtx, id)
					if err != nil || sesCtx.Err() != nil {
						return
					}
					send(msgSnapshot{*s})
				}()
			}
		}()
	}

	// reconnect sends msgReconnect via AfterFunc so connect() runs in the event loop.
	reconnect := func() {
		d := a.cfg.Backoff.Next()
		a.cfg.Clock.AfterFunc(d, func() {
			send(msgReconnect{})
		})
	}

	connect() // initial connection attempt

	for {
		select {
		case <-ctx.Done():
			if currentCancel != nil {
				currentCancel()
			}
			return

		case m := <-a.msgCh:
			switch msg := m.(type) {

			case msgReconnect:
				connect()

			case msgConnected:
				if msg.gen != connectGen {
					break // stale connect from a cancelled session
				}
				state = state.applyConnected(msg.caps)
				a.cfg.Backoff.Reset()
				notify()

			case msgDisconnected:
				if msg.gen != connectGen {
					break // stale disconnect from a previous session
				}
				if currentCancel != nil {
					currentCancel()
					currentCancel = nil
				}
				currentSesCtx = nil
				if state.connected {
					state = state.applyDisconnected()
					notify()
				}
				reconnect()

			case msgRepoList:
				state = state.applyRepoList(msg.summaries)
				notify()

			case msgSnapshot:
				state = state.applySnapshot(msg.status)
				notify()

			case msgEvent:
				newState, needsResync, dirtyID := state.applyEvent(msg.ev)
				state = newState
				if needsResync {
					fullResync()
				} else if dirtyID != "" {
					dirtyRepos[dirtyID] = true
					if debounceTimer == nil {
						debounceTimer = a.cfg.Clock.AfterFunc(a.cfg.Debounce, func() {
							send(msgFlushDirty{})
						})
					}
				}

			case msgFlushDirty:
				debounceTimer = nil
				sesCtx := currentSesCtx // capture at dispatch time
				if sesCtx != nil {
					for id := range dirtyRepos {
						repoID := id
						go func() {
							s, err := a.cfg.Client.RepoStatus(sesCtx, repoID)
							if err != nil || sesCtx.Err() != nil {
								return
							}
							send(msgSnapshot{*s})
						}()
					}
				}
				dirtyRepos = make(map[string]bool)
			}

		case <-periodic.C:
			fullResync()
		}
	}
}

func (a *App) drainEvents(sesCtx context.Context, evCh <-chan contract.Event, send func(appMsg)) {
	for {
		select {
		case ev, ok := <-evCh:
			if !ok {
				return
			}
			select {
			case a.msgCh <- msgEvent{ev}:
			case <-sesCtx.Done():
				return
			}
		case <-sesCtx.Done():
			return
		}
	}
}

func hasCap(caps []string, cap string) bool {
	for _, c := range caps {
		if c == cap {
			return true
		}
	}
	return false
}
