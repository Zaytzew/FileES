package watcher

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileDeletePreparationDoesNotAcknowledgeAndRecoveryPreservesRecreation(t *testing.T) {
	s, wc := treeMoveScanner(t)
	before, _ := os.ReadFile(s.statePath)
	var saved FileDeleteSnapshot
	if err := s.WithFileDeletePreparation(t.Context(), "outside", func(p *FileDeleteSnapshot) error {
		if s.scanMu.TryLock() {
			s.scanMu.Unlock()
			t.Fatal("prepare outside scanner fence")
		}
		saved = *p
		return os.Remove(filepath.Join(wc, "outside"))
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(s.statePath)
	if !bytes.Equal(before, after) {
		t.Fatal("preparation acknowledged OS permission as publication")
	}
	if err := os.WriteFile(filepath.Join(wc, "outside"), []byte("new unpublished recreation"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wc, "źródło", "two"), []byte("unrelated unpublished edit of a different size"), 0600); err != nil {
		t.Fatal(err)
	}
	failed := errors.New("remote outcome not proved")
	if err := s.WithFileDeleteRecovery(t.Context(), &saved, func() error { return failed }); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(s.statePath)
	if !bytes.Equal(before, after) {
		t.Fatal("uncertain deletion advanced baseline")
	}
	if err := s.WithFileDeleteRecovery(t.Context(), &saved, func() error {
		if s.scanMu.TryLock() {
			t.Fatal("recovery outside scanner fence")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, scanner := range []*Scanner{s, publicationScanner(t, wc)} {
		got := map[string]OpType{}
		for _, e := range publicationScan(t, scanner) {
			got[e.Rel] = e.Op
		}
		if got["outside"] != Added || got["źródło/two"] != Modified || len(got) != 2 {
			t.Fatal("later work lost or deletion replayed", got)
		}
	}
	if !s.EventAcknowledged(Event{Session: saved.Session, Sequence: saved.Sequence, Rel: saved.Source}) ||
		s.EventAcknowledged(Event{Session: saved.Session, Sequence: saved.Sequence + 1, Rel: saved.Source}) ||
		s.EventAcknowledged(Event{Session: saved.Session, Sequence: saved.Sequence, Rel: "źródło/two"}) {
		t.Fatal("event acknowledgement widened")
	}
	if err := s.WithFileDeleteRecovery(t.Context(), &saved, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := publicationScan(t, s); len(got) != 0 {
		t.Fatal("receipt replay rolled observed work back", got)
	}
}

func TestFileDeleteAbsentBaselineAndRefusals(t *testing.T) {
	s, wc := treeMoveScanner(t)
	var p FileDeleteSnapshot
	if err := s.WithFileDeletePreparation(t.Context(), "remote-only.bin", func(in *FileDeleteSnapshot) error { p = *in; return nil }); err != nil || p.Entry.Present {
		t.Fatal(p, err)
	}
	// No local entry is required for a virtual file, but it does not authorize
	// deletion; the independent coordinator must supply completion and receipt.
	called := false
	if err := s.WithFileDeleteRecovery(t.Context(), &p, func() error { called = true; return errors.New("no completion") }); err == nil || !called {
		t.Fatal(err)
	}
	for _, rel := range []string{".", "../outside", ".filees/state", "x\\y", "x:stream", "x.", "x ", "źródło"} {
		if err := s.WithFileDeletePreparation(t.Context(), rel, func(*FileDeleteSnapshot) error { t.Fatal("unsafe or directory admitted", rel); return nil }); err == nil {
			t.Fatal(rel)
		}
	}
	if err := s.WithFileDeletePreparation(t.Context(), "outside", func(in *FileDeleteSnapshot) error { in.Entry.Size++; return nil }); err == nil {
		t.Fatal("mutated snapshot accepted")
	}
	if err := s.WithFileDeletePreparation(t.Context(), "outside", func(in *FileDeleteSnapshot) error { p = *in; return errors.New("prepared only") }); err == nil {
		t.Fatal(err)
	}
	base, _ := s.treeMoveBaseline()
	e := base["outside"]
	e.Size++
	base["outside"] = e
	if _, err := fileDeleteBaselinePhase(base, &p); err == nil {
		t.Fatal("replaced baseline accepted")
	}
	delete(base, "outside")
	base["OUTSIDE"] = e
	if _, err := fileDeleteBaselinePhase(base, &p); err == nil {
		t.Fatal("case alias accepted")
	}
	p.WC = filepath.Join(wc, "other")
	if err := s.WithFileDeleteRecovery(t.Context(), &p, func() error { t.Fatal("foreign snapshot consulted"); return nil }); err == nil {
		t.Fatal("foreign WC accepted")
	}
}
