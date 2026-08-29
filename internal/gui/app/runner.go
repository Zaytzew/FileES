package app

import (
	"context"
	"sync"
	"time"

	contract "filees/pkg/contract/v1"
)

// Clock abstracts timers so reconnect, debounce and periodic refresh are
// deterministic in tests.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) clockTimer
}

type clockTimer interface {
	Stop() bool
}

type realClock struct{}

func (realClock) Now() time.Time                                 { return time.Now() }
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
	Periodic time.Duration // full refresh interval; default 30s; negative disables it
}

// App is the GUI presentation model. Call Run to start the event loop.
// All state mutations and session-generation comparisons happen in that loop.
type App struct {
	cfg   Config
	msgCh chan appMsg
	once  sync.Once
}

// Reconnect requests an immediate fresh daemon session. It is safe to call
// from UI goroutines; all cancellation and state changes happen in the event
// loop. A duplicate request may be coalesced when the message queue is full.
func (a *App) Reconnect() {
	select {
	case a.msgCh <- msgManualReconnect{}:
	default:
	}
}

// Refresh requests an immediate full snapshot without tearing down the live
// daemon session. Mutation actions use it to reflect new lock reservations in
// the tray before the periodic refresh interval expires.
func (a *App) Refresh() {
	select {
	case a.msgCh <- msgRefreshNow{}:
	default:
	}
}

// StartAction projects a user gesture immediately. The action remains visible
// until FinishAction cancels/fails it or AwaitActionProjection fences it to a
// full snapshot started after the daemon accepted the mutation.
func (a *App) StartAction(action PendingAction) bool {
	select {
	case a.msgCh <- msgActionStart{action: action}:
		return true
	default:
		return false
	}
}

func (a *App) AwaitActionProjection(id string) {
	select {
	case a.msgCh <- msgActionAwait{id: id}:
	default:
	}
}

func (a *App) FinishAction(id string) {
	select {
	case a.msgCh <- msgActionFinish{id: id}:
	default:
	}
}

// New creates an App. cfg.Client must be non-nil.
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

type msgConnected struct {
	gen  int
	caps []string
}
type msgDisconnected struct{ gen int }
type msgReconnect struct{ gen int }
type msgManualReconnect struct{}
type msgPeriodic struct{}
type msgRefreshNow struct{}
type msgActionStart struct{ action PendingAction }
type msgActionAwait struct{ id string }
type msgActionFinish struct{ id string }
type msgFullSnapshot struct {
	gen                   int
	system                contract.SystemStatusResult
	summaries             []contract.RepoSummary
	statuses              []contract.RepoStatus
	errors                []contract.ErrorRecord
	activity              []contract.ActivityRecord
	reservationCounts     map[string]int
	repoReservationCounts map[string]int
	reservations          []Reservation
	reservationsKnown     bool
	notices               []contract.Notice
	publicShares          []contract.PublicShareSummary
	publicSharesKnown     bool
	refreshed             time.Time
	actionFences          []string
}
type msgPartialSnapshots struct {
	gen      int
	statuses []contract.RepoStatus
}
type msgEvent struct {
	gen int
	ev  contract.Event
}
type msgFlushDirty struct{ gen int }

func (msgConnected) sealed()        {}
func (msgDisconnected) sealed()     {}
func (msgReconnect) sealed()        {}
func (msgManualReconnect) sealed()  {}
func (msgPeriodic) sealed()         {}
func (msgRefreshNow) sealed()       {}
func (msgActionStart) sealed()      {}
func (msgActionAwait) sealed()      {}
func (msgActionFinish) sealed()     {}
func (msgFullSnapshot) sealed()     {}
func (msgPartialSnapshots) sealed() {}
func (msgEvent) sealed()            {}
func (msgFlushDirty) sealed()       {}

// --- event loop ---

func (a *App) loop(ctx context.Context) {
	state := newAppState()

	var (
		connectGen     int
		currentCancel  context.CancelFunc
		currentSesCtx  context.Context
		reconnectTimer clockTimer
		periodicTimer  clockTimer
		debounceTimer  clockTimer

		refreshInFlight      bool
		fullPending          bool
		dirtyRepos           = make(map[string]bool)
		actionFencesPending  = make(map[string]bool)
		actionFencesInFlight []string
	)

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

	stopTimer := func(timer *clockTimer) {
		if *timer != nil {
			(*timer).Stop()
			*timer = nil
		}
	}

	var schedulePeriodic func()
	schedulePeriodic = func() {
		if a.cfg.Periodic < 0 || ctx.Err() != nil {
			return
		}
		periodicTimer = a.cfg.Clock.AfterFunc(a.cfg.Periodic, func() {
			send(msgPeriodic{})
		})
	}

	var launchFullRefresh func()
	var launchDirtyRefresh func()
	var finishRefresh func()

	launchFullRefresh = func() {
		if currentSesCtx == nil {
			return
		}
		if refreshInFlight {
			fullPending = true
			return
		}

		refreshInFlight = true
		fullPending = false
		dirtyRepos = make(map[string]bool)
		stopTimer(&debounceTimer)
		sesCtx := currentSesCtx
		gen := connectGen
		includeErrors := state.caps[contract.CapErrorList]
		includeActivity := state.caps[contract.CapRepoActivity]
		includeReservations := state.caps[contract.CapRepoReservationList]
		includeNotices := state.caps[contract.CapNoticeList]
		includePublicShares := state.caps[contract.CapRepoPublicShareList]
		fences := make([]string, 0, len(actionFencesPending))
		for id := range actionFencesPending {
			fences = append(fences, id)
		}
		actionFencesPending = make(map[string]bool)
		actionFencesInFlight = fences

		go func() {
			system, err := a.cfg.Client.SystemStatus(sesCtx)
			if err != nil {
				a.sendSessionFailure(sesCtx, gen, send)
				return
			}
			list, err := a.cfg.Client.RepoList(sesCtx)
			if err != nil {
				a.sendSessionFailure(sesCtx, gen, send)
				return
			}

			statuses := make([]contract.RepoStatus, 0, len(list.Repos))
			for _, repo := range list.Repos {
				status, err := a.cfg.Client.RepoStatus(sesCtx, repo.ID)
				if err != nil {
					a.sendSessionFailure(sesCtx, gen, send)
					return
				}
				statuses = append(statuses, *status)
			}
			var errors []contract.ErrorRecord
			if includeErrors {
				result, err := a.cfg.Client.ErrorList(sesCtx, contract.ErrorListPayload{Limit: 20})
				if err != nil {
					a.sendSessionFailure(sesCtx, gen, send)
					return
				}
				errors = result.Errors
			}
			var activityRecords []contract.ActivityRecord
			if includeActivity {
				if client, ok := a.cfg.Client.(interface {
					RepoActivity(context.Context, int) (*contract.RepoActivityResult, error)
				}); ok {
					result, err := client.RepoActivity(sesCtx, 20)
					if err != nil {
						a.sendSessionFailure(sesCtx, gen, send)
						return
					}
					activityRecords = result.Entries
				}
			}
			reservationCounts := make(map[string]int)
			repoReservationCounts := make(map[string]int)
			reservations := make([]Reservation, 0)
			reservationsKnown := false
			if includeReservations {
				reservationsKnown = true
				for _, activation := range system.Activations {
					result, err := a.cfg.Client.RepoReservationList(sesCtx, activation.ServerID)
					if err != nil {
						// The inventory is supplemental presentation data. A
						// temporary failure must not take down the primary GUI
						// session; keep the header action safely disabled instead.
						reservationsKnown = false
						break
					}
					reservationCounts[activation.ServerID] = len(result.Reservations)
					for _, reservation := range result.Reservations {
						repoReservationCounts[reservationKey(activation.ServerID, reservation.RepoID)]++
						reservations = append(reservations, projectReservation(activation.ServerID, reservation))
					}
				}
				if !reservationsKnown {
					reservations = nil
				}
			}
			var notices []contract.Notice
			if includeNotices {
				if client, ok := a.cfg.Client.(interface {
					NoticeList(context.Context) (*contract.NoticeListResult, error)
				}); ok {
					result, err := client.NoticeList(sesCtx)
					if err != nil {
						a.sendSessionFailure(sesCtx, gen, send)
						return
					}
					notices = result.Notices
				}
			}
			var publicShares []contract.PublicShareSummary
			publicSharesKnown := false
			if includePublicShares {
				if client, ok := a.cfg.Client.(interface {
					PublicShareListAll(context.Context) (*contract.PublicShareListResult, error)
				}); ok {
					// The aggregate command was added after the per-repository
					// capability. Older daemons legitimately advertise the latter
					// but reject list-all, so this supplemental presenter must not
					// tear down an otherwise healthy GUI session.
					if result, err := client.PublicShareListAll(sesCtx); err == nil {
						publicShares = result.Shares
						publicSharesKnown = true
					}
				}
			}
			if sesCtx.Err() == nil {
				send(msgFullSnapshot{gen: gen, system: *system, summaries: list.Repos,
					statuses: statuses, errors: errors, activity: activityRecords,
					reservationCounts: reservationCounts, repoReservationCounts: repoReservationCounts,
					reservations: reservations, reservationsKnown: reservationsKnown,
					notices: notices, publicShares: publicShares, publicSharesKnown: publicSharesKnown,
					refreshed: a.cfg.Clock.Now(), actionFences: fences})
			}
		}()
	}

	launchDirtyRefresh = func() {
		if currentSesCtx == nil || refreshInFlight || len(dirtyRepos) == 0 {
			return
		}
		refreshInFlight = true
		ids := make([]string, 0, len(dirtyRepos))
		for id := range dirtyRepos {
			ids = append(ids, id)
		}
		dirtyRepos = make(map[string]bool)
		sesCtx := currentSesCtx
		gen := connectGen

		go func() {
			statuses := make([]contract.RepoStatus, 0, len(ids))
			for _, id := range ids {
				status, err := a.cfg.Client.RepoStatus(sesCtx, id)
				if err != nil {
					a.sendSessionFailure(sesCtx, gen, send)
					return
				}
				statuses = append(statuses, *status)
			}
			if sesCtx.Err() == nil {
				send(msgPartialSnapshots{gen: gen, statuses: statuses})
			}
		}()
	}

	finishRefresh = func() {
		refreshInFlight = false
		actionFencesInFlight = nil
		if fullPending {
			launchFullRefresh()
			return
		}
		if len(dirtyRepos) > 0 && debounceTimer == nil {
			launchDirtyRefresh()
		}
	}

	scheduleReconnect := func(gen int) {
		stopTimer(&reconnectTimer)
		delay := a.cfg.Backoff.Next()
		reconnectTimer = a.cfg.Clock.AfterFunc(delay, func() {
			send(msgReconnect{gen: gen})
		})
	}

	connect := func() {
		stopTimer(&reconnectTimer)
		connectGen++
		gen := connectGen
		if currentCancel != nil {
			currentCancel()
		}
		sesCtx, sesCancel := context.WithCancel(ctx)
		currentCancel = sesCancel
		currentSesCtx = sesCtx

		go func() {
			hello, err := a.cfg.Client.Hello(sesCtx)
			if err != nil {
				a.sendSessionFailure(sesCtx, gen, send)
				return
			}

			var events <-chan contract.Event
			if hasCap(hello.Capabilities, contract.CapEventsSubscribe) {
				events, err = a.cfg.Client.Subscribe(sesCtx)
				if err != nil {
					a.sendSessionFailure(sesCtx, gen, send)
					return
				}
			}

			send(msgConnected{gen: gen, caps: hello.Capabilities})
			if events != nil {
				a.drainEvents(sesCtx, gen, events, send)
				if sesCtx.Err() == nil {
					send(msgDisconnected{gen: gen})
				}
			}
		}()
	}

	schedulePeriodic()
	connect()

	defer func() {
		if currentCancel != nil {
			currentCancel()
		}
		stopTimer(&reconnectTimer)
		stopTimer(&periodicTimer)
		stopTimer(&debounceTimer)
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case raw := <-a.msgCh:
			switch msg := raw.(type) {
			case msgManualReconnect:
				for _, id := range actionFencesInFlight {
					actionFencesPending[id] = true
				}
				actionFencesInFlight = nil
				stopTimer(&reconnectTimer)
				if currentCancel != nil {
					currentCancel()
				}
				currentCancel = nil
				currentSesCtx = nil
				refreshInFlight = false
				fullPending = false
				dirtyRepos = make(map[string]bool)
				stopTimer(&debounceTimer)
				a.cfg.Backoff.Reset()
				state = state.applyDisconnected()
				notify()
				connect()

			case msgReconnect:
				if msg.gen == connectGen && currentSesCtx == nil {
					connect()
				}

			case msgConnected:
				if msg.gen != connectGen || currentSesCtx == nil {
					break
				}
				state = state.applyConnected(msg.caps)
				notify()
				launchFullRefresh()

			case msgDisconnected:
				if msg.gen != connectGen || currentSesCtx == nil {
					break
				}
				if currentCancel != nil {
					currentCancel()
				}
				for _, id := range actionFencesInFlight {
					actionFencesPending[id] = true
				}
				actionFencesInFlight = nil
				currentCancel = nil
				currentSesCtx = nil
				refreshInFlight = false
				fullPending = false
				dirtyRepos = make(map[string]bool)
				stopTimer(&debounceTimer)
				// Register the timer before publishing disconnected, so observers
				// using a fake clock cannot advance before it exists.
				scheduleReconnect(msg.gen)
				state = state.applyDisconnected()
				notify()

			case msgFullSnapshot:
				if msg.gen != connectGen || currentSesCtx == nil {
					break
				}
				state = state.applyFullSnapshot(msg.system, msg.summaries, msg.statuses, msg.errors, msg.activity, msg.reservationCounts, msg.repoReservationCounts, msg.reservations, msg.reservationsKnown, msg.notices, msg.publicShares, msg.publicSharesKnown, msg.refreshed)
				var waiting []string
				state, waiting = state.confirmPendingActions(msg.actionFences)
				for _, id := range waiting {
					actionFencesPending[id] = true
				}
				a.cfg.Backoff.Reset()
				notify()
				finishRefresh()

			case msgPartialSnapshots:
				if msg.gen != connectGen || currentSesCtx == nil {
					break
				}
				state = state.applySnapshots(msg.statuses)
				notify()
				finishRefresh()

			case msgEvent:
				if msg.gen != connectGen || currentSesCtx == nil {
					break
				}
				var needsResync bool
				var dirtyID string
				state, needsResync, dirtyID = state.applyEvent(msg.ev)
				if needsResync {
					state = state.applyStale()
					notify()
					launchFullRefresh()
					break
				}
				if msg.ev.Type == contract.EvActivityChanged {
					launchFullRefresh()
					break
				}
				if dirtyID == "" {
					launchFullRefresh()
					break
				}
				dirtyRepos[dirtyID] = true
				if debounceTimer == nil {
					gen := connectGen
					debounceTimer = a.cfg.Clock.AfterFunc(a.cfg.Debounce, func() {
						send(msgFlushDirty{gen: gen})
					})
				}

			case msgFlushDirty:
				if msg.gen != connectGen || currentSesCtx == nil {
					break
				}
				debounceTimer = nil
				launchDirtyRefresh()

			case msgPeriodic:
				periodicTimer = nil
				if currentSesCtx != nil {
					launchFullRefresh()
				}
				schedulePeriodic()

			case msgRefreshNow:
				launchFullRefresh()

			case msgActionStart:
				state = state.startPendingAction(msg.action)
				notify()

			case msgActionAwait:
				if _, exists := state.pendingActions[msg.id]; exists {
					state = state.awaitPendingAction(msg.id)
					if _, stillPending := state.pendingActions[msg.id]; !stillPending {
						delete(actionFencesPending, msg.id)
						notify()
						break
					}
					actionFencesPending[msg.id] = true
					notify()
					launchFullRefresh()
				}

			case msgActionFinish:
				state = state.finishPendingActions([]string{msg.id})
				delete(actionFencesPending, msg.id)
				notify()
			}
		}
	}
}

func (a *App) sendSessionFailure(sesCtx context.Context, gen int, send func(appMsg)) {
	if sesCtx.Err() == nil {
		send(msgDisconnected{gen: gen})
	}
}

func (a *App) drainEvents(sesCtx context.Context, gen int, events <-chan contract.Event, send func(appMsg)) {
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			send(msgEvent{gen: gen, ev: ev})
		case <-sesCtx.Done():
			return
		}
	}
}

func hasCap(caps []string, capability string) bool {
	for _, candidate := range caps {
		if candidate == capability {
			return true
		}
	}
	return false
}
