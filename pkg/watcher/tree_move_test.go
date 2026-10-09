package watcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestShutdownSaveDoesNotAdvancePendingMutationBaseline(t *testing.T) {
	s := publicationScanner(t, t.TempDir())
	publicationScan(t, s)
	before, err := os.ReadFile(s.statePath)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.cur["unpublished"] = meta{Size: 100}
	s.mu.Unlock()
	s.publicationPending = func() bool { return true }
	if err := s.SaveState(s.statePath); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.statePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("shutdown acknowledged unconfirmed work", err)
	}
}

func treeMoveScanner(t *testing.T) (*Scanner, string) {
	t.Helper()
	wc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wc, "źródło", "子"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"źródło/子/one", "źródło/two", "outside"} {
		if err := os.WriteFile(filepath.Join(wc, filepath.FromSlash(name)), []byte("published seed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := publicationScanner(t, wc)
	publicationScan(t, s)
	if _, err := os.ReadFile(s.statePath); err != nil {
		t.Fatal(err)
	}
	return s, wc
}

func readTreeSnapshot(t *testing.T, raw []byte) *TreeMoveSnapshot {
	t.Helper()
	var p TreeMoveSnapshot
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

func TestTreeMoveBaselinePreservesEditsAndNewWorkAcrossRestart(t *testing.T) {
	s, wc := treeMoveScanner(t)
	var persisted []byte
	var sequence uint64
	if err := s.WithTreeMove(t.Context(), "źródło", "cel", func(p *TreeMoveSnapshot) error {
		if s.scanMu.TryLock() {
			s.scanMu.Unlock()
			t.Fatal("scan escaped tree fence")
		}
		sequence = p.Sequence
		var err error
		persisted, err = json.Marshal(p)
		if err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(wc, "źródło"), filepath.Join(wc, "cel")); err != nil {
			return err
		}
		for _, name := range []string{"cel/子/one", "cel/added", "outside"} {
			if err := os.WriteFile(filepath.Join(wc, filepath.FromSlash(name)), []byte("unpublished later work of a different length"), 0600); err != nil {
				return err
			}
		}
		return nil // Fixture proves its exact rename. Production also proves SVN/CFAPI.
	}); err != nil {
		t.Fatal(err)
	}
	base, err := s.treeMoveBaseline()
	if err != nil || base["cel/子/one"].Size != int64(len("published seed")) || base["outside"].Size != int64(len("published seed")) {
		t.Fatal("edits acknowledged as publication", base, err)
	}
	if _, ok := base["źródło/子/one"]; ok {
		t.Fatal("old baseline survived")
	}
	for _, name := range []string{"źródło/子/one", "źródło/untracked-before-move", "cel/子/one"} {
		if !s.EventAcknowledged(Event{Session: s.session, Sequence: sequence, Rel: name}) {
			t.Fatal("prior selected event not fenced", name)
		}
		if s.EventAcknowledged(Event{Session: s.session, Sequence: sequence + 1, Rel: name}) {
			t.Fatal("newer event swallowed", name)
		}
	}
	if s.EventAcknowledged(Event{Session: s.session, Sequence: sequence, Rel: "outside"}) {
		t.Fatal("unrelated event swallowed")
	}
	for _, current := range []*Scanner{s, publicationScanner(t, wc)} {
		got := map[string]OpType{}
		for _, e := range publicationScan(t, current) {
			got[e.Rel] = e.Op
		}
		if len(got) != 3 || got["cel/子/one"] != Modified || got["cel/added"] != Added || got["outside"] != Modified {
			t.Fatalf("lost edits or repeated move: %+v", got)
		}
	}
	// Idempotent receipt replay must not reset observed later work in RAM.
	if err := s.WithTreeMoveRecovery(t.Context(), readTreeSnapshot(t, persisted), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := publicationScan(t, s); len(got) != 0 {
		t.Fatal("receipt replay rolled observed work back", got)
	}
}

func TestUncertainTreeMoveDoesNotAckAndRecoveryUsesSavedBaseline(t *testing.T) {
	s, wc := treeMoveScanner(t)
	before, err := os.ReadFile(s.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var saved []byte
	uncertain := errors.New("local receipt lost")
	if err := s.WithTreeMove(t.Context(), "źródło", "cel", func(p *TreeMoveSnapshot) error {
		saved, _ = json.Marshal(p)
		if err := os.Rename(filepath.Join(wc, "źródło"), filepath.Join(wc, "cel")); err != nil {
			return err
		}
		return uncertain
	}); !errors.Is(err, uncertain) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.statePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("uncertain effect acknowledged", err)
	}
	if err := os.WriteFile(filepath.Join(wc, "cel", "two"), []byte("edited after crash"), 0600); err != nil {
		t.Fatal(err)
	}
	r := publicationScanner(t, wc)
	r.publicationPending = func() bool { return true }
	publicationScan(t, r) // No rescan is permission to replace the saved baseline.
	if err := r.WithTreeMoveRecovery(t.Context(), readTreeSnapshot(t, saved), func() error {
		if r.scanMu.TryLock() {
			r.scanMu.Unlock()
			t.Fatal("recovery not fenced")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.publicationPending = nil
	r = publicationScanner(t, wc)
	got := publicationScan(t, r)
	if len(got) != 1 || got[0].Rel != "cel/two" || got[0].Op != Modified {
		t.Fatal("recovery swallowed edit or replayed move", got)
	}
}

func TestTreeMoveRejectsChangedBaselineAndMutatedSnapshot(t *testing.T) {
	for _, collision := range []string{"cel/foreign", "źródło/extra", "ŹRÓDŁO/case-alias"} {
		t.Run(collision, func(t *testing.T) {
			s, _ := treeMoveScanner(t)
			var p *TreeMoveSnapshot
			if err := s.WithTreeMove(t.Context(), "źródło", "cel", func(in *TreeMoveSnapshot) error {
				p = cloneTreeMoveSnapshot(in)
				return errors.New("stop before effect")
			}); err == nil {
				t.Fatal("fixture")
			}
			base, _ := s.treeMoveBaseline()
			base[collision] = diskEntry{Path: collision, Size: 1}
			var list []diskEntry
			for _, e := range base {
				list = append(list, e)
			}
			if err := s.writeJSON(s.statePath, list); err != nil {
				t.Fatal(err)
			}
			called := false
			if err := s.WithTreeMoveRecovery(t.Context(), p, func() error { called = true; return nil }); err == nil || called {
				t.Fatal("changed baseline reached effect proof", err)
			}
		})
	}
	s, _ := treeMoveScanner(t)
	before, _ := os.ReadFile(s.statePath)
	if err := s.WithTreeMove(t.Context(), "źródło", "cel", func(p *TreeMoveSnapshot) error { p.Entries[0].Mtime++; return nil }); err == nil {
		t.Fatal("mutated observations accepted")
	}
	after, _ := os.ReadFile(s.statePath)
	if !bytes.Equal(before, after) {
		t.Fatal("mutated snapshot changed baseline")
	}
	for _, paths := range [][2]string{{"źródło", "źródło/sub"}, {"źródło", "ŹRÓDŁO"}, {".filees", "cel"}, {"źródło", "cel\tbad"}, {"źródło", "cel."}, {"../source", "cel"}, {"absent", "cel"}} {
		if err := s.WithTreeMove(t.Context(), paths[0], paths[1], func(*TreeMoveSnapshot) error { t.Fatal("invalid selection reached effect"); return nil }); err == nil {
			t.Fatal(paths)
		}
	}
}

func TestTreeMoveCancellationAndBackgroundReaders(t *testing.T) {
	s, _ := treeMoveScanner(t)
	before, _ := os.ReadFile(s.statePath)
	ctx, cancel := context.WithCancel(t.Context())
	if err := s.WithTreeMove(ctx, "źródło", "cel", func(*TreeMoveSnapshot) error { cancel(); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(s.statePath)
	if !bytes.Equal(before, after) {
		t.Fatal("canceled receipt acknowledged")
	}
	if err := s.WithTreeMove(ctx, "źródło", "cel", func(*TreeMoveSnapshot) error { t.Fatal("canceled effect ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	started, done := make(chan struct{}), make(chan struct{})
	if err := s.WithTreeMove(t.Context(), "źródło", "cel", func(*TreeMoveSnapshot) error {
		go func() { close(started); s.processOneBacklogItem(t.Context()); s.SaveState(s.statePath); close(done) }()
		<-started
		select {
		case <-done:
			t.Fatal("background reader/shutdown escaped fence")
		case <-time.After(20 * time.Millisecond):
		}
		return errors.New("no effect")
	}); err == nil {
		t.Fatal("fixture")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fence leaked")
	}
}

func TestTreeMoveReannouncesUnpublishedAddAndDeletionAtDestination(t *testing.T) {
	s, wc := treeMoveScanner(t)
	if err := os.WriteFile(filepath.Join(wc, "źródło", "untracked"), []byte("new local work"), 0600); err != nil {
		t.Fatal(err)
	}
	queued := publicationScan(t, s)
	if len(queued) != 1 || queued[0].Op != Added {
		t.Fatal(queued)
	}
	if err := s.WithTreeMove(t.Context(), "źródło", "cel", func(*TreeMoveSnapshot) error {
		if err := os.Rename(filepath.Join(wc, "źródło"), filepath.Join(wc, "cel")); err != nil {
			return err
		}
		return os.Remove(filepath.Join(wc, "cel", "two"))
	}); err != nil {
		t.Fatal(err)
	}
	if !s.EventAcknowledged(queued[0]) {
		t.Fatal("old-path add left in queue")
	}
	got := map[string]OpType{}
	for i := 0; i < 2; i++ {
		for _, e := range publicationScan(t, s) {
			got[e.Rel] = e.Op
		}
	}
	if len(got) != 2 || got["cel/untracked"] != Added || got["cel/two"] != Deleted {
		t.Fatal("lost local add/delete or repeated move", got)
	}
	r := publicationScanner(t, wc)
	got = map[string]OpType{}
	for i := 0; i < 2; i++ {
		for _, e := range publicationScan(t, r) {
			got[e.Rel] = e.Op
		}
	}
	if len(got) != 2 || got["cel/untracked"] != Added || got["cel/two"] != Deleted {
		t.Fatal("restart swallowed local work", got)
	}
}

func TestTreeMoveRecoveryRejectsUnboundOrDamagedObservations(t *testing.T) {
	s, _ := treeMoveScanner(t)
	var p *TreeMoveSnapshot
	s.WithTreeMove(t.Context(), "źródło", "cel", func(got *TreeMoveSnapshot) error { p = cloneTreeMoveSnapshot(got); return errors.New("not sent") })
	for _, mutate := range []func(*TreeMoveSnapshot){
		func(p *TreeMoveSnapshot) { p.WC = filepath.Join(p.WC, "foreign") },
		func(p *TreeMoveSnapshot) { p.Schema = "future" },
		func(p *TreeMoveSnapshot) { p.Sequence = 0 },
		func(p *TreeMoveSnapshot) { p.Sequence = s.sequence + 1 },
		func(p *TreeMoveSnapshot) { p.Entries = append(p.Entries, p.Entries[0]) },
		func(p *TreeMoveSnapshot) { p.Entries[0].Present = false },
		func(p *TreeMoveSnapshot) { p.Entries[0].Size = -1 },
		func(p *TreeMoveSnapshot) { p.Entries[0].MD5 = "fake directory content" },
	} {
		bad := cloneTreeMoveSnapshot(p)
		mutate(bad)
		if err := s.WithTreeMoveRecovery(t.Context(), bad, func() error { t.Fatal("damaged snapshot reached proof"); return nil }); err == nil {
			t.Fatal("bad observations accepted")
		}
	}
	before, _ := os.ReadFile(s.statePath)
	want := errors.New("local object proof differs")
	if err := s.WithTreeMoveRecovery(t.Context(), p, func() error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(s.statePath)
	if !bytes.Equal(before, after) {
		t.Fatal("failed proof acknowledged")
	}
}
