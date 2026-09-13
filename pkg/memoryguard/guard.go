// Package memoryguard implements a last-resort policy, not leak prevention.
package memoryguard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"filees/internal/durable"
)

type Sample struct {
	PrivateBytes   uint64 `json:"private_bytes"`
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	HeapBytes      uint64 `json:"heap_bytes"`
	Goroutines     int    `json:"goroutines"`
}

type Status struct {
	Phase       string    `json:"phase"`
	Sample      Sample    `json:"sample"`
	At          time.Time `json:"at"`
	LastRestart time.Time `json:"last_restart,omitempty"`
}

type Guard struct {
	Path       string
	Sample     func() (Sample, error)
	Publish    func(Status)
	Diagnostic func(error)
	// Stopped reports that worker cancellation has already begun. A failed
	// finalizer is terminal for this process: keep its warning, never re-admit.
	Stopped func() bool
	// Restart must close ingress and drain before invoking checkpoint, then
	// stop/join all workers. An error must never trigger process replacement.
	Restart     func(context.Context, func() error) error
	Cleanup     func()
	Now         func() time.Time
	Interval    time.Duration
	status      Status
	critical    int
	lastCleanup time.Time
	blocked     bool
}

const Cooldown = 6 * time.Hour

func Thresholds(total uint64) (uint64, uint64) {
	const mib = uint64(1024 * 1024)
	return max(256*mib, min(1024*mib, total/10)), max(512*mib, min(2048*mib, total/5))
}

func (g *Guard) publish(phase string, sample Sample) {
	g.status.Phase, g.status.Sample, g.status.At = phase, sample, g.now()
	if g.Publish != nil {
		g.Publish(g.status)
	}
}
func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Guard) Load() {
	b, err := os.ReadFile(g.Path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err == nil {
		err = json.Unmarshal(b, &g.status)
	}
	if err != nil {
		g.blocked = true
		g.diagnostic(err)
	}
}

func (g *Guard) diagnostic(err error) {
	if g.Diagnostic != nil && err != nil {
		g.Diagnostic(err)
	}
}

func (g *Guard) checkpoint() error {
	g.status.LastRestart = g.now()
	b, err := json.Marshal(g.status)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(g.Path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(g.Path), ".memory-restart-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, g.Path); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(g.Path))
}

// Step requires three consecutive critical samples, and persists the restart
// attempt before stopping work. Clock rollback and corrupt receipts fail closed.
func (g *Guard) Step(ctx context.Context) bool {
	sample, err := g.Sample()
	if g.Stopped != nil && g.Stopped() {
		g.publish("recovery_required", sample)
		return false
	}
	if err != nil || sample.TotalBytes == 0 {
		g.diagnostic(err)
		g.critical = 0
		g.publish("unavailable", Sample{})
		return false
	}
	warn, critical := Thresholds(sample.TotalBytes)
	if sample.PrivateBytes < warn*3/4 {
		g.critical = 0
		g.publish("normal", sample)
		return false
	}
	if sample.PrivateBytes < warn {
		g.critical = 0
		return false
	}
	g.publish("warning", sample)
	if g.lastCleanup.IsZero() || g.now().Sub(g.lastCleanup) >= 30*time.Minute {
		g.lastCleanup = g.now()
		if g.Cleanup != nil {
			g.Cleanup()
		}
		// Re-measure next tick: cleanup may have relieved transient pressure.
		g.critical = 0
		return false
	}
	if sample.PrivateBytes < critical {
		g.critical = 0
		return false
	}
	g.critical++
	if g.critical < 3 {
		return false
	}
	if g.blocked || (!g.status.LastRestart.IsZero() && g.now().Sub(g.status.LastRestart) < Cooldown) {
		g.publish("cooldown", sample)
		return false
	}
	if g.Restart == nil {
		g.publish("deferred", sample)
		return false
	}
	g.publish("draining", sample)
	if err := g.Restart(ctx, g.checkpoint); err != nil {
		g.diagnostic(err)
		if g.Stopped != nil && g.Stopped() {
			g.publish("recovery_required", sample)
		} else {
			g.publish("deferred", sample)
		}
		return false
	}
	g.publish("restarting", sample)
	return true
}

func (g *Guard) Run(ctx context.Context) {
	if g.Sample == nil {
		g.Sample = ReadSample
	}
	if g.Cleanup == nil {
		g.Cleanup = debug.FreeOSMemory
	}
	if g.Interval <= 0 {
		g.Interval = time.Minute
	}
	g.Load()
	ticker := time.NewTicker(g.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if g.Step(ctx) {
				return
			}
		}
	}
}

func addRuntime(s Sample) Sample {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	s.HeapBytes, s.Goroutines = m.HeapAlloc, runtime.NumGoroutine()
	return s
}
