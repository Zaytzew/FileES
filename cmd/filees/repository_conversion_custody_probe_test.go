//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"filees/pkg/localrepo"
)

func TestConversionNativeSourceReadBudget(t *testing.T) {
	for _, baseline := range []bool{false, true} {
		t.Run(fmt.Sprintf("rehash_baseline=%v", baseline), func(t *testing.T) {
			conversionNativeSourceReadBudget(t, baseline)
		})
	}
}

// Force loss of only the test's ephemeral root binding at the old phase
// boundaries to exercise the full-rehash fallback on the same real dataset.
// There is no production switch that can skip custody validation.
type conversionRehashBaseline struct{ *nativeConversionEffects }

func (b conversionRehashBaseline) forget() {
	if b.old != nil {
		b.old.Root = ""
	}
}
func (b conversionRehashBaseline) Retain(ctx context.Context, r localrepo.ConversionRecord) error {
	b.forget()
	return b.nativeConversionEffects.Retain(ctx, r)
}
func (b conversionRehashBaseline) Preserve(ctx context.Context, r localrepo.ConversionRecord) error {
	b.forget()
	return b.nativeConversionEffects.Preserve(ctx, r)
}
func (b conversionRehashBaseline) Verify(ctx context.Context, r localrepo.ConversionRecord) error {
	b.forget()
	return b.nativeConversionEffects.Verify(ctx, r)
}

func conversionNativeSourceReadBudget(t *testing.T, baseline bool) {
	t.Helper()
	f := newConversionNativeRunFixture(t, 64)
	cli := probeTool(t, "FILEES_PROBE_SVN")
	random := rand.New(rand.NewSource(1979))
	data := make([]byte, 4<<20)
	for i := 0; i < 64; i++ {
		if _, err := random.Read(data); err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(f.record.LocalPath, "folder", fmt.Sprintf("drawing%02d.dwg", i))
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	probeRun(t, f.directory, cli, "commit", "-q", "-m", "isolated read budget", f.record.LocalPath)
	probeRun(t, f.directory, cli, "update", "-q", f.record.LocalPath)
	var observed atomic.Int64
	factory := f.p.newConversionEffects
	f.p.newConversionEffects = func(r localrepo.Record, intent localrepo.ConversionRecord) (conversionEffectsAdapter, error) {
		a, err := factory(r, intent)
		if err == nil {
			native := a.(*nativeConversionEffects)
			native.observeSourceRead = func(n int64) { observed.Add(n) }
			if baseline {
				return conversionRehashBaseline{native}, nil
			}
		}
		return a, err
	}
	start := time.Now()
	current, ok := f.p.local.Get(f.record.OperationID)
	if !ok {
		t.Fatal("missing conversion attachment")
	}
	operation := current.ConversionID
	daemonConversionFixture{p: f.p, r: f.record, events: f.events}.runWithin(t, t.Context(), 2*time.Minute)
	f.assertComplete(t)
	var plan conversionFSPlan
	raw, err := os.ReadFile(filepath.Join(f.p.conversionRoot, operation, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	var size int64
	for _, node := range plan.Objects {
		if !node.Directory {
			size += node.Size
		}
	}
	passes := int64(3)
	if baseline {
		passes = 7
	}
	if observed.Load() != passes*size {
		t.Fatalf("source validation read budget: got %d want %d (%d passes of %d)", observed.Load(), passes*size, passes, size)
	}
	t.Logf("source bytes=%d validation bytes=%d passes=%d conversion elapsed=%s; zero hydration", size, observed.Load(), passes, time.Since(start))
}
