package localallocation

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func awaitValue(t *testing.T, m *Manager, server string, accept func(Value) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if accept(m.Values()[server]) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("unexpected allocation: %+v", m.Values()[server])
}

func TestProgressiveStartupRetainsUnseenCachedRoots(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "cache.json"))
	a := Root{Server: "s", Repo: "a", Path: t.TempDir()}
	b := Root{Server: "s", Repo: "b", Path: t.TempDir()}
	m.save(map[string]record{
		rootKey(a): {Value: Value{Known: true, Bytes: 10, State: "ready"}},
		rootKey(b): {Value: Value{Known: true, Bytes: 20, State: "ready"}},
	})
	m.Update([]Root{a}, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx, nil) }()
	defer func() { cancel(); <-done }()
	awaitValue(t, m, "s", func(v Value) bool { return v.Known && v.Bytes == 10 })
	m.Update([]Root{a, b}, true)
	awaitValue(t, m, "s", func(v Value) bool { return v.Known && v.Bytes == 30 && v.State == "stale" })
	// A transient empty projection does not invalidate the persisted bindings.
	m.Update(nil, true)
	if len(m.Values()) != 0 {
		t.Fatal("inactive bindings counted")
	}
	m.Update([]Root{b}, true)
	awaitValue(t, m, "s", func(v Value) bool { return v.Known && v.Bytes == 20 })
	// A different binding must not inherit the previous path's measurement.
	b.Path = t.TempDir()
	m.Update([]Root{b}, true)
	if m.Values()["s"].Known {
		t.Fatal("moved binding inherited cache")
	}
}

func TestMissingRootKeepsMeasuredSubtotalVisible(t *testing.T) {
	m := New("")
	a := Root{Server: "s", Repo: "a", Path: t.TempDir()}
	b := Root{Server: "s", Repo: "b", Path: t.TempDir()}
	m.publish(rootKey(a), Value{Known: true, Bytes: 42, State: "stale"}, nil)
	m.Update([]Root{a, b}, true)
	if v := m.Values()["s"]; !v.Known || v.Bytes != 42 || v.State != "partial" {
		t.Fatalf("measured subtotal hidden or presented as complete: %+v", v)
	}
	m.Update([]Root{b}, true)
	if m.Values()["s"].Known {
		t.Fatal("no measured roots reported known")
	}
}

func TestCachePressureEvictsDormantNotActiveBindings(t *testing.T) {
	m := New("")
	a := Root{Server: "s", Repo: "active", Path: t.TempDir()}
	records := map[string]record{rootKey(a): {Value: Value{Known: true}}}
	for i := range maxCachedRoots {
		key := fmt.Sprintf("dormant-%04d", i)
		records[key] = record{Value: Value{Known: true, MeasuredAt: fmt.Sprintf("%04d", i)}}
		m.results[key] = records[key].Value
	}
	if !m.trimDormant(records, []Root{a}) || len(records) != maxCachedRoots {
		t.Fatal("cache was not bounded")
	}
	if _, ok := records[rootKey(a)]; !ok {
		t.Fatal("active binding evicted")
	}
	if _, ok := records["dormant-0000"]; ok {
		t.Fatal("oldest dormant record retained")
	}
	if _, ok := m.results["dormant-0000"]; ok {
		t.Fatal("evicted value retained in memory")
	}
}

func TestShutdownPersistsLastCompletedMeasurement(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "cache.json"))
	a := Root{Server: "a", Repo: "a", Path: t.TempDir()}
	b := Root{Server: "b", Repo: "b", Path: t.TempDir()}
	m.Update([]Root{a, b}, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx, nil) }()
	defer func() { cancel(); <-done }()
	awaitValue(t, m, "b", func(v Value) bool { return v.Known })
	cancel()
	<-done
	if !m.load()[rootKey(b)].Value.Known {
		t.Fatal("last completed measurement lost before minute checkpoint")
	}
}
