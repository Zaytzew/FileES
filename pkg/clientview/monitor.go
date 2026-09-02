package clientview

import (
	"context"
	"time"
)

type MonitorConfig struct {
	Sync     SyncConfig
	Interval time.Duration
	OnError  func(error)
	// OnSync fires after every sync that succeeded, including one that found
	// nothing new. The channel below cannot carry that fact: it emits only on
	// a changed generation, so a stable server produces silence that is
	// indistinguishable from a server which has stopped answering. A caller
	// tracking how fresh its data is needs the difference, and only this hook
	// has it.
	OnSync func(View)
}

// Monitor emits a complete validated view whenever a newer generation is
// atomically cached. It never emits partial state and stops with ctx.
func Monitor(ctx context.Context, updater Updater, config MonitorConfig) <-chan View {
	out := make(chan View, 1)
	interval := config.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		defer close(out)
		syncOnce := func() {
			view, changed, err := Sync(ctx, updater, config.Sync)
			if err != nil {
				if ctx.Err() == nil && config.OnError != nil {
					config.OnError(err)
				}
				return
			}
			if config.OnSync != nil {
				config.OnSync(view)
			}
			if !changed {
				return
			}
			select {
			case out <- view:
			case <-ctx.Done():
			}
		}
		syncOnce()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				syncOnce()
			}
		}
	}()
	return out
}
