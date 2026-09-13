package memoryguard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSustainedPressureCheckpointAndCooldown(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	sample := Sample{PrivateBytes: 3 << 30, TotalBytes: 16 << 30}
	clean, restarted := 0, 0
	g := Guard{Path: filepath.Join(t.TempDir(), "memory.json"), Now: func() time.Time { return now }, Sample: func() (Sample, error) { return sample, nil }, Cleanup: func() { clean++ }}
	g.Restart = func(ctx context.Context, checkpoint func() error) error {
		if err := checkpoint(); err != nil {
			return err
		}
		if _, err := os.Stat(g.Path); err != nil {
			t.Fatal(err)
		}
		restarted++
		return nil
	}
	for i := 0; i < 3; i++ {
		if g.Step(t.Context()) {
			t.Fatal("premature restart")
		}
		now = now.Add(time.Minute)
	}
	if !g.Step(t.Context()) || restarted != 1 || clean != 1 {
		t.Fatalf("restart=%d clean=%d", restarted, clean)
	}
	second := Guard{Path: g.Path, Now: g.Now, Sample: g.Sample, Restart: g.Restart}
	second.Load()
	for i := 0; i < 8; i++ {
		now = now.Add(time.Minute)
		if second.Step(t.Context()) {
			t.Fatal("restart loop")
		}
	}
	if second.status.Phase != "cooldown" || restarted != 1 {
		t.Fatal(second.status)
	}
	// Recovery needs a substantially lower reading; unknown metrics never restart.
	sample.PrivateBytes = 100 << 20
	second.Step(t.Context())
	if second.status.Phase != "normal" {
		t.Fatal(second.status)
	}
	second.Sample = func() (Sample, error) { return Sample{}, errors.New("missing metric") }
	second.Step(t.Context())
	if second.status.Phase != "unavailable" {
		t.Fatal(second.status)
	}
}

func TestFailedDrainAndCorruptReceiptNeverRestart(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "memory.json")
		if corrupt {
			if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		calls := 0
		g := Guard{Path: path, Sample: func() (Sample, error) { return Sample{PrivateBytes: 3 << 30, TotalBytes: 16 << 30}, nil }, Restart: func(context.Context, func() error) error { calls++; return errors.New("busy") }}
		g.Load()
		for i := 0; i < 6; i++ {
			if g.Step(t.Context()) {
				t.Fatal("unsafe restart")
			}
		}
		if corrupt && calls != 0 {
			t.Fatal("corrupt receipt allowed attempt")
		}
		if !corrupt {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("drain failure wrote receipt: %v", err)
			}
		}
	}
}

func TestCheckpointFailureAndClockRollbackFailClosed(t *testing.T) {
	dir := t.TempDir()
	block := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(block, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	g := Guard{Path: filepath.Join(block, "memory.json"), Sample: func() (Sample, error) { return Sample{PrivateBytes: 3 << 30, TotalBytes: 16 << 30}, nil }}
	g.Restart = func(_ context.Context, checkpoint func() error) error { return checkpoint() }
	for i := 0; i < 5; i++ {
		if g.Step(t.Context()) {
			t.Fatal("restart despite failed durable receipt")
		}
	}
	if g.status.LastRestart.IsZero() {
		t.Fatal("attempt not retained in memory")
	}
	g.Now = func() time.Time { return g.status.LastRestart.Add(-time.Hour) }
	if g.Step(t.Context()) || g.status.Phase != "cooldown" {
		t.Fatal(g.status)
	}
}
