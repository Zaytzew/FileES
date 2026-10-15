package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeFeaturesSharedAcrossRealChildLaunches(t *testing.T) {
	first := raFake(t, `{"schema":"filees.native-svn/v1","ok":true}`)
	trace := filepath.Join(t.TempDir(), "versions")
	t.Setenv("FILEES_TEST_VERSION_TRACE", trace)
	cache := &nativeFeatureCache{}
	var wg sync.WaitGroup
	for range 24 {
		c := New(Options{NativeSVNPath: first.nativeSVNPath, Timeout: 10 * time.Second}).(*execClient)
		c.featureCache = cache
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.nativeRequireFeature(t.Context(), "writer_lease_v1"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	raw, err := os.ReadFile(trace)
	if err != nil || strings.Count(string(raw), "probe\n") != 1 {
		t.Fatalf("probes=%q: %v", raw, err)
	}
}

func capabilityReceipt(names ...string) map[string]any {
	list := make([]any, len(names))
	for i, s := range names {
		list[i] = s
	}
	return map[string]any{"schema": nativeSchema, "ok": true, "features": list}
}

func featureImage(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(p, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNativeFeaturesCoalescesAcrossClients(t *testing.T) {
	cache := &nativeFeatureCache{}
	p := featureImage(t)
	var calls atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			f, e := cache.get(t.Context(), p, func(context.Context) (map[string]any, error) {
				calls.Add(1)
				time.Sleep(15 * time.Millisecond)
				return capabilityReceipt("writer_inspect_v1"), nil
			})
			if e != nil || !f["writer_inspect_v1"] {
				t.Errorf("%v %v", f, e)
			}
		}()
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("probes=%d", calls.Load())
	}
}

func TestNativeFeaturesInvalidatesChangedAndMissingImage(t *testing.T) {
	cache := &nativeFeatureCache{}
	p := featureImage(t)
	calls := 0
	probe := func(context.Context) (map[string]any, error) {
		calls++
		return capabilityReceipt(fmt.Sprint(calls)), nil
	}
	if _, e := cache.get(t.Context(), p, probe); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte("new larger image"), 0600); e != nil {
		t.Fatal(e)
	}
	f, e := cache.get(t.Context(), p, probe)
	if e != nil || !f["2"] || f["1"] {
		t.Fatal(f, e)
	}
	if e := os.Remove(p); e != nil {
		t.Fatal(e)
	}
	if _, e := cache.get(t.Context(), p, probe); e == nil {
		t.Fatal("missing image reused")
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestNativeFeaturesFailuresAndChangedDuringProbeNotCached(t *testing.T) {
	for _, mode := range []string{"error", "malformed", "changed"} {
		t.Run(mode, func(t *testing.T) {
			cache := &nativeFeatureCache{}
			p := featureImage(t)
			calls := 0
			probe := func(context.Context) (map[string]any, error) {
				calls++
				if calls == 1 {
					switch mode {
					case "error":
						return nil, context.DeadlineExceeded
					case "malformed":
						return map[string]any{"features": []any{1}}, nil
					case "changed":
						if e := os.WriteFile(p, []byte("changed image"), 0600); e != nil {
							t.Fatal(e)
						}
					}
				}
				return capabilityReceipt("ok"), nil
			}
			if _, e := cache.get(t.Context(), p, probe); e == nil {
				t.Fatal("bad probe accepted")
			}
			if f, e := cache.get(t.Context(), p, probe); e != nil || !f["ok"] {
				t.Fatal(f, e)
			}
			if calls != 2 {
				t.Fatal(calls)
			}
		})
	}
}

func TestNativeFeaturesWaiterCancellationAndFailedLeader(t *testing.T) {
	cache := &nativeFeatureCache{}
	p := featureImage(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, e := cache.get(t.Context(), p, func(context.Context) (map[string]any, error) { close(entered); <-release; return nil, context.Canceled })
		done <- e
	}()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, e := cache.get(ctx, p, func(context.Context) (map[string]any, error) { t.Error("duplicate probe"); return nil, nil }); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	close(release)
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if f, e := cache.get(t.Context(), p, func(context.Context) (map[string]any, error) { return capabilityReceipt("ok"), nil }); e != nil || !f["ok"] {
		t.Fatal(f, e)
	}
}

func TestNativeFeaturesBoundedCache(t *testing.T) {
	cache := &nativeFeatureCache{}
	dir := t.TempDir()
	for i := range 80 {
		p := filepath.Join(dir, fmt.Sprint(i))
		if e := os.WriteFile(p, []byte("x"), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := cache.get(t.Context(), p, func(context.Context) (map[string]any, error) { return capabilityReceipt("ok"), nil }); e != nil {
			t.Fatal(e)
		}
	}
	if len(cache.entries) > 64 {
		t.Fatal(len(cache.entries))
	}
}
