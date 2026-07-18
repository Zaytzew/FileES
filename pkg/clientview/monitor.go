package clientview

import (
	"context"
	"time"
)

type MonitorConfig struct {
	Sync     SyncConfig
	Interval time.Duration
	OnError  func(error)
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
